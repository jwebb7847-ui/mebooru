package desktop

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeUsesFixedHealthRequestAndStopsAtRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/health" {
			t.Errorf("unexpected probe: %s %s", r.Method, r.URL)
			return
		}
		http.Redirect(w, r, "/other", http.StatusFound)
	}))
	defer server.Close()

	addr := strings.TrimPrefix(server.URL, "http://")
	if inst, found := Probe(addr, time.Second); !found || inst.App != "" {
		t.Fatalf("redirect probe returned %+v, found=%t", inst, found)
	}
	if _, found := Probe("example.invalid:80", time.Second); found {
		t.Fatal("unexpected probe outside local interfaces")
	}
}
