package session

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLocalRedirectPaths(t *testing.T) {
	for _, p := range []string{"/", "/images/1", "/settings?tab=general", "/search?q=tag", "/local#anchor"} {
		if !LocalPath(p) {
			t.Errorf("rejected local path %q", p)
		}
	}
	for _, p := range []string{"", "relative", "https://example.invalid/", "//example.invalid", "///example.invalid", "/\\example.invalid", "/a\\b", "/\t/example.invalid", "/\n/example.invalid", "/\r/example.invalid", "/\x00x", "/\x7fx"} {
		if LocalPath(p) {
			t.Errorf("accepted unsafe redirect %q", p)
		}
	}
}

func TestSessionLifetimeAndCSRF(t *testing.T) {
	s := New()
	a, b := s.Create(1), s.Create(1)
	if a == b || a == "" || !s.Valid(a) || !s.Valid(b) || s.Valid("unknown") {
		t.Fatal("invalid session creation")
	}
	token := s.CSRFToken(a)
	if !s.ValidCSRF(a, token) || s.ValidCSRF(b, token) || s.ValidCSRF(a, "") || New().ValidCSRF(a, token) {
		t.Fatal("CSRF token was not bound to session and store")
	}
	if !s.ValidCSRF(Anon, s.CSRFToken(Anon)) {
		t.Fatal("anonymous CSRF token failed")
	}
	expired := s.Create(-1)
	if s.Valid(expired) {
		t.Fatal("expired session accepted")
	}
	expired = s.Create(-1)
	s.SweepExpired()
	if _, ok := s.expires[expired]; ok {
		t.Fatal("expired session not swept")
	}
	s.ClearExcept(a)
	if !s.Valid(a) || s.Valid(b) {
		t.Fatal("ClearExcept failed")
	}
	s.Delete(a)
	if s.Valid(a) {
		t.Fatal("deleted session accepted")
	}
	c := s.Create(1)
	s.Clear()
	if s.Valid(c) {
		t.Fatal("cleared session accepted")
	}
}

func TestSessionGateAndGuard(t *testing.T) {
	s := New()
	id := s.Create(1)
	called := false
	handler := s.Gate(s.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}), nil), "test_session", func() bool { return true }, func(p string) bool { return p == "/login" })
	for _, tc := range []struct {
		name, method, path, cookie, csrf, origin string
		htmx bool
		want int
	}{
		{"unauthenticated", "GET", "/settings?x=1", "", "", "", false, 303},
		{"htmx unauthenticated", "GET", "/settings", "", "", "", true, 401},
		{"valid cookie", "GET", "/settings", id, "", "", false, 204},
		{"invalid cookie", "GET", "/settings", "invalid", "", "", false, 303},
		{"no csrf", "POST", "/settings", id, "", "", false, 403},
		{"wrong csrf", "POST", "/settings", id, s.CSRFToken("other"), "", false, 403},
		{"foreign origin", "POST", "/settings", id, "", "http://example.invalid", false, 403},
		{"valid csrf", "POST", "/settings", id, s.CSRFToken(id), "", false, 204},
		{"same origin fallback", "POST", "/settings", id, "", "http://127.0.0.1:8455", false, 204},
		{"public login", "GET", "/login", "", "", "", false, 204},
		{"API delegated to bearer gate", "POST", "/api/v1/test", "", "", "", false, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			r := httptest.NewRequest(tc.method, "http://127.0.0.1:8455"+tc.path, nil)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "test_session", Value: tc.cookie})
			}
			if tc.csrf != "" {
				r.Header.Set("X-CSRF-Token", tc.csrf)
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.htmx {
				r.Header.Set("HX-Request", "true")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == 204) {
				t.Fatalf("status=%d handler called=%v, want %d", w.Code, called, tc.want)
			}
			if tc.name == "unauthenticated" {
				u, err := url.Parse(w.Header().Get("Location"))
				if err != nil || u.Path != "/login" || u.Query().Get("next") != tc.path {
					t.Fatal("login redirect did not preserve local path")
				}
			}
		})
	}
	s.Delete(id)
	r := httptest.NewRequest("GET", "http://127.0.0.1:8455/settings", nil)
	r.AddCookie(&http.Cookie{Name: "test_session", Value: id})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatal("deleted session passed gate")
	}
}

func TestForwardedHeadersAndCookies(t *testing.T) {
	for _, tc := range []struct {
		remote, target, ip string
		secure bool
	}{
		{"192.0.2.10:1234", "http://localhost/", "192.0.2.10", false},
		{"127.0.0.1:1234", "http://localhost/", "198.51.100.2", true},
		{"[::1]:1234", "http://localhost/", "198.51.100.2", true},
		{"192.0.2.10:1234", "https://localhost/", "192.0.2.10", true},
	} {
		r := httptest.NewRequest("GET", tc.target, nil)
		r.RemoteAddr = tc.remote
		r.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.2")
		r.Header.Set("X-Forwarded-Proto", "https")
		if ClientIP(r) != tc.ip || RequestIsHTTPS(r) != tc.secure {
			t.Fatalf("wrong trust result for %s", tc.remote)
		}
		w := httptest.NewRecorder()
		SetCookie(w, r, "test_session", "fixture", 60)
		c := w.Result().Cookies()
		if len(c) != 1 || !c[0].HttpOnly || c[0].Secure != tc.secure || c[0].SameSite != http.SameSiteLaxMode || c[0].Path != "/" || c[0].MaxAge != 60 {
			t.Fatal("unexpected cookie attributes")
		}
	}
}

func TestCSRFFormAndMultipart(t *testing.T) {
	s := New()
	form := url.Values{"_csrf": {s.CSRFToken(Anon)}}.Encode()
	r := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !s.CheckCSRF(r, Anon) {
		t.Fatal("valid form token refused")
	}
	r = httptest.NewRequest("POST", "http://localhost/", strings.NewReader("not a parsed multipart body"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=fixture")
	if s.CheckCSRF(r, Anon) || r.MultipartForm != nil {
		t.Fatal("multipart without header token was accepted or parsed")
	}
	r.Header.Set("X-CSRF-Token", s.CSRFToken(Anon))
	if !s.CheckCSRF(r, Anon) || r.MultipartForm != nil {
		t.Fatal("multipart header token not honored without parsing")
	}
}

func TestLoginBackoff(t *testing.T) {
	b := NewBackoff()
	b.Fail("peer")
	if w := b.Wait("peer"); w <= 0 || w > time.Second {
		t.Fatalf("unexpected initial delay %v", w)
	}
	for i := 0; i < 10; i++ {
		b.Fail("peer")
	}
	if w := b.Wait("peer"); w <= 0 || w > 30*time.Second {
		t.Fatalf("unexpected capped delay %v", w)
	}
	if b.Wait("other") != 0 {
		t.Fatal("backoff leaked between peers")
	}
	b.Succeed("peer")
	if b.Wait("peer") != 0 {
		t.Fatal("success did not reset backoff")
	}
}
