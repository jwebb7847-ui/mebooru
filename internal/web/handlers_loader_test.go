package web

import (
	"net/http/httptest"
	"testing"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/peer"
)

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
