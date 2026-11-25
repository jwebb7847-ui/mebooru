package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Instance struct {
	App     string `json:"app"`
	Version string `json:"version"`
}

const maxHealthBody = 4 << 10

// Probe checks for an instance on this machine using a fixed health request.
// A local interface address supports installs that bind to a specific LAN IP.
func Probe(addr string, timeout time.Duration) (inst Instance, found bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return Instance{}, false
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return Instance{}, false
	}
	if strings.EqualFold(host, "localhost") {
		start := time.Now()
		if inst, found := Probe(net.JoinHostPort("127.0.0.1", port), timeout); found {
			return inst, true
		}
		if remaining := timeout - time.Since(start); remaining > 0 {
			return Probe(net.JoinHostPort("::1", port), remaining)
		}
		return Instance{}, false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !localProbeIP(ip) {
		return Instance{}, false
	}
	destination := netip.AddrPortFrom(ip, uint16(number)).String()
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, destination)
			},
		},
	}
	resp, err := client.Get("http://localhost/health")
	if err != nil {
		return Instance{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Instance{}, true
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHealthBody))
	if err != nil {
		return Instance{}, true
	}
	_ = json.Unmarshal(body, &inst)
	return inst, true
}

func localProbeIP(ip netip.Addr) bool {
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil || ip.Zone() != "" {
		return false
	}
	for _, addr := range addrs {
		if prefix, err := netip.ParsePrefix(addr.String()); err == nil && prefix.Addr().Unmap() == ip.Unmap() {
			return true
		}
	}
	return false
}

func LoopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// LocalRequest is false on a proxy header, since behind a same-host proxy every peer
// is loopback, and on a non-loopback Host or a browser's Origin or Referer naming one,
// which a default proxy does not rewrite. A plain GET through such a proxy still passes.
func LocalRequest(r *http.Request) bool {
	if !IsLoopbackAddr(r.Host) {
		return false
	}
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP", "Via"} {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" && !originIsLoopback(v) {
			return false
		}
	}
	return IsLoopbackAddr(r.RemoteAddr)
}

func originIsLoopback(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && IsLoopbackAddr(u.Host)
}

// A page that rebinds its own name to 127.0.0.1 sends that name, and with
// no password it would otherwise drive every route. A network bind serves LAN names
// too, so it refuses only a name public DNS could answer, and never on the API, whose
// bearer token a rebinding page does not hold.
func ForeignHost(r *http.Request, bindAddr, baseURL string) bool {
	if IsLoopbackAddr(r.Host) || sameHost(r.Host, baseURL) {
		return false
	}
	if IsLoopbackAddr(bindAddr) {
		return true
	}
	return publicName(r.Host) && r.URL.Path != "/health" && !strings.HasPrefix(r.URL.Path, "/api/v1/")
}

var privateSuffixes = []string{
	".localhost", ".local", ".lan", ".home", ".internal", ".intranet", ".corp", ".home.arpa", ".ts.net",
}

func publicName(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return false
	}
	return !slices.ContainsFunc(privateSuffixes, func(s string) bool { return strings.HasSuffix(host, s) })
}

func sameHost(host, baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	return strings.EqualFold(strings.Trim(h, "[]"), u.Hostname())
}

func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
