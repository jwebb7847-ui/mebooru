package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/monloader"
	"github.com/monbooru/monbooru/internal/peer"
	"github.com/monbooru/monbooru/internal/shared/apitoken"
)

func TestLoaderResultLinksOnlyToRecordedGallery(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	item := monloader.ImportItem{PostID: "123", Outcome: contract.OutcomeCreated, SHA256: sha}
	row := buildLoaderResult("Audit_A", item, 1)
	if row.URL != "/i/"+sha+"?gallery=Audit_A" || row.PostID != "123" {
		t.Fatalf("created result = %+v", row)
	}
	if row := buildLoaderResult("", item, 1); row.URL != "" {
		t.Fatalf("linked to an unspecified gallery: %+v", row)
	}
	item.Outcome = contract.OutcomeSkippedBlacklist
	if row := buildLoaderResult("Audit_A", item, 1); row.URL != "" || row.Label != "Blacklisted" {
		t.Fatalf("blacklisted result = %+v", row)
	}
}

func TestLoaderPanelUsesSeparateConfiguredPeerAndGallery(t *testing.T) {
	s := &Server{cfg: config.Default()}
	s.cfg.Server.MonloaderURL = "http://127.0.0.1:8456/"
	s.cfg.Monloader.APIToken = "secret"
	r := httptest.NewRequest("GET", "http://127.0.0.1:8455/loader?view=ptr", nil)
	base := baseData{MonloaderPaired: true, ActiveGallery: "Audit_A"}

	for _, tc := range []struct{ path, want string }{
		{"/queue", "http://127.0.0.1:8456/loader/queue?embed=1&gallery=Audit_A"},
		{"/watches", "http://127.0.0.1:8456/watches?embed=1"},
		{"/ptr", "http://127.0.0.1:8456/ptr?embed=1"},
		{"/settings", "http://127.0.0.1:8456/settings?embed=1"},
	} {
		if got := s.loaderPanel(r, base, tc.path); got.FrameURL != tc.want || got.Message != "" {
			t.Fatalf("%s panel = %+v", tc.path, got)
		}
	}
	base.ActiveGallery = "Audit_B"
	if got := s.loaderPanel(r, base, "/queue").FrameURL; got != "http://127.0.0.1:8456/loader/queue?embed=1&gallery=Audit_B" {
		t.Fatalf("gallery switch left frame at %q", got)
	}
	for _, conn := range []string{peer.ConnPaused, peer.ConnDown, peer.ConnRejected} {
		base.MonloaderConn = conn
		if got := s.loaderPanel(r, base, "/queue"); got.FrameURL != "" || got.Message == "" {
			t.Fatalf("%s panel = %+v", conn, got)
		}
	}
	base.MonloaderPaired = false
	if got := s.loaderPanel(r, base, "/settings"); got.FrameURL != "" || got.OpenURL != "" || got.Message == "" {
		t.Fatalf("unpaired panel = %+v", got)
	}
}

func TestLoaderPeerURLRejectsUnsafeFrameOrigins(t *testing.T) {
	for _, raw := range []string{
		"", "javascript:alert(1)", "//meloader.test", "https://operator:secret@meloader.test",
		"https://meloader.test/prefix", "https://meloader.test/?x=y", "https://meloader.test/#fragment",
		"http://meloader.test", "https://MEBOORU.test:443",
	} {
		if got := loaderPeerURL(raw, "https", "mebooru.test"); got != "" {
			t.Errorf("%q accepted as %q", raw, got)
		}
	}
	if got := loaderPeerURL("https://meloader.test:8456", "https", "mebooru.test"); got != "https://meloader.test:8456" {
		t.Errorf("separate HTTPS origin = %q", got)
	}
	if got := gallerySwitchTarget("/loader?view=ptr"); got != "/loader?view=ptr" {
		t.Errorf("Loader switch target = %q", got)
	}
}

func TestLoaderPanelFallsBackOnlyForLocalHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, parent, client, api, web, forwarded, want string
	}{
		{name: "paired address", want: "http://127.0.0.1:8456"},
		{name: "API override", api: "http://localhost:19456", want: "http://localhost:19456"},
		{name: "web override", web: "http://127.0.0.1:19456", want: "http://127.0.0.1:19456"},
		{name: "same origin", api: "http://127.0.0.1:8455"},
		{name: "remote API", api: "http://meloader.internal:8456"},
		{name: "remote parent", parent: "http://mebooru.internal:8455"},
		{name: "remote client", client: "192.0.2.10:1234"},
		{name: "proxy", forwarded: "127.0.0.1"},
		{name: "HTTPS", parent: "https://127.0.0.1:8455"},
		{name: "invalid explicit web URL", web: "http://127.0.0.1:8456/not-an-origin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: config.Default()}
			s.cfg.Auth.Tokens = apitoken.List{{Paired: monloader.App, PeerURL: "http://127.0.0.1:8456"}}
			s.cfg.Monloader.APIURL = tc.api
			s.cfg.Server.MonloaderURL = tc.web
			parent := tc.parent
			if parent == "" {
				parent = "http://127.0.0.1:8455"
			}
			r := httptest.NewRequest("GET", parent+"/loader?view=advanced", nil)
			r.RemoteAddr = tc.client
			if r.RemoteAddr == "" {
				r.RemoteAddr = "127.0.0.1:1234"
			}
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			base := baseData{MonloaderPaired: true, ActiveGallery: "Audit_A"}
			got := s.loaderPanel(r, base, "/queue")
			if tc.want == "" {
				if got.FrameURL != "" || got.OpenURL != "" || !got.Setup {
					t.Fatalf("expected address setup, got %+v", got)
				}
				return
			}
			if got.FrameURL != tc.want+"/loader/queue?embed=1&gallery=Audit_A" || got.Setup {
				t.Fatalf("panel = %+v", got)
			}
			if got := s.loaderPanel(r, base, "/settings"); got.FrameURL != tc.want+"/settings?embed=1" {
				t.Fatalf("settings panel = %+v", got)
			}
		})
	}
}
