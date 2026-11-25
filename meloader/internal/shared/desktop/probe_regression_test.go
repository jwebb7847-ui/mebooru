package desktop

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeRecognizesLocalHealthAndOccupiedPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want Instance
	}{
		{"healthy", 200, "{\"app\":\"fixture\",\"version\":\"1\"}", Instance{App: "fixture", Version: "1"}},
		{"not health", 404, "not found", Instance{}},
		{"invalid JSON", 200, "not JSON", Instance{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.RequestURI() != "/health" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tc.code)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			addr := server.Listener.Addr().String()
			if got, found := Probe(addr, time.Second); !found || got != tc.want {
				t.Fatalf("probe=%+v found=%v; want %+v", got, found, tc.want)
			}
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				t.Fatal(err)
			}
			if got, found := Probe(net.JoinHostPort("localhost", port), time.Second); !found || got != tc.want {
				t.Fatalf("localhost probe=%+v found=%v; want %+v", got, found, tc.want)
			}
			listener, err := Listen("fixture", addr, 0)
			if listener != nil {
				listener.Close()
				t.Fatal("occupied port was acquired")
			}
			if tc.want.App == "fixture" {
				if !errors.Is(err, ErrRunning) {
					t.Fatalf("same-app handover not recognized: %v", err)
				}
			} else if err == nil || errors.Is(err, ErrRunning) {
				t.Fatalf("foreign process not refused: %v", err)
			}
		})
	}
}

func TestProbeDoesNotFollowRedirectToAnotherListener(t *testing.T) {
	var called atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		_, _ = fmt.Fprint(w, "{\"app\":\"fixture\"}")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/health", http.StatusFound)
	}))
	defer redirector.Close()
	if got, found := Probe(redirector.Listener.Addr().String(), time.Second); !found || got != (Instance{}) {
		t.Fatalf("unexpected redirect result: %+v %v", got, found)
	}
	if called.Load() != 0 {
		t.Fatal("probe followed redirect")
	}
}

func TestProbeRejectsNonlocalAndInvalidAddresses(t *testing.T) {
	for _, addr := range []string{
		"example.invalid:80", "192.0.2.123:80", "127.0.0.1:0",
		"127.0.0.1:65536", "127.0.0.1:http", "127.0.0.1",
		"127.0.0.1:80/path", "http://127.0.0.1:80",
	} {
		if got, found := Probe(addr, time.Second); found || got != (Instance{}) {
			t.Errorf("unexpected probe result for %q: %+v %v", addr, got, found)
		}
	}
}

func TestProbeLocalInterfaceAddresses(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range addrs {
		prefix, err := netip.ParsePrefix(addr.String())
		if err != nil || !prefix.Addr().Is4() || prefix.Addr().IsLoopback() {
			continue
		}
		ip := prefix.Addr()
		if !localProbeIP(ip) {
			t.Fatalf("local interface refused: %v", ip)
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
		if err != nil {
			continue
		}
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, "{\"app\":\"fixture\"}")
		}))
		server.Listener.Close()
		server.Listener = ln
		server.Start()
		defer server.Close()
		if got, found := Probe(ln.Addr().String(), time.Second); !found || got.App != "fixture" {
			t.Fatalf("local interface probe failed: %+v %v", got, found)
		}
		return
	}
	t.Skip("no bindable non-loopback IPv4 interface")
}

func TestProbeIPv6Loopback(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "{\"app\":\"fixture\"}")
	}))
	server.Listener.Close()
	server.Listener = ln
	server.Start()
	defer server.Close()
	if got, found := Probe(ln.Addr().String(), time.Second); !found || got.App != "fixture" {
		t.Fatalf("IPv6 probe failed: %+v %v", got, found)
	}
}

func TestLocalRequestAndForeignHostBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, host, remote, header, value string
		want bool
	}{
		{"loopback", "localhost:8455", "127.0.0.1:321", "", "", true},
		{"IPv6", "[::1]:8455", "[::1]:321", "", "", true},
		{"external host", "example.invalid", "127.0.0.1:321", "", "", false},
		{"external peer", "localhost:8455", "192.0.2.10:321", "", "", false},
		{"proxy", "localhost:8455", "127.0.0.1:321", "Forwarded", "for=127.0.0.1", false},
		{"foreign origin", "localhost:8455", "127.0.0.1:321", "Origin", "https://example.invalid", false},
		{"foreign referer", "localhost:8455", "127.0.0.1:321", "Referer", "https://example.invalid/", false},
		{"local origin", "localhost:8455", "127.0.0.1:321", "Origin", "http://localhost:8455", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/", nil)
			r.RemoteAddr = tc.remote
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			if got := LocalRequest(r); got != tc.want {
				t.Fatalf("LocalRequest=%v; want %v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		host, path, bind, base string
		want bool
	}{
		{"example.invalid", "/", "127.0.0.1:8455", "", true},
		{"localhost:8455", "/", "127.0.0.1:8455", "", false},
		{"example.invalid", "/", "0.0.0.0:8455", "", true},
		{"device.local", "/", "0.0.0.0:8455", "", false},
		{"example.invalid", "/health", "0.0.0.0:8455", "", false},
		{"example.invalid", "/api/v1/images", "0.0.0.0:8455", "", false},
		{"library.example.invalid", "/", "0.0.0.0:8455", "https://library.example.invalid", false},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+tc.path, nil)
		if got := ForeignHost(r, tc.bind, tc.base); got != tc.want {
			t.Errorf("ForeignHost(%s %s)=%v; want %v", tc.host, tc.path, got, tc.want)
		}
	}
}

func TestLoopbackAddrWildcardConversion(t *testing.T) {
	for _, addr := range []string{":8455", "0.0.0.0:8455", "[::]:8455"} {
		if got := LoopbackAddr(addr); got != "127.0.0.1:8455" {
			t.Errorf("LoopbackAddr(%q)=%q", addr, got)
		}
	}
	if got := LoopbackAddr("[::1]:8455"); !strings.HasPrefix(got, "[::1]:") {
		t.Fatalf("IPv6 loopback changed: %q", got)
	}
}
