package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/monbooru/monbooru/internal/peer"
	"github.com/monbooru/monbooru/internal/shared/session"
)

type loaderPanel struct {
	FrameURL string
	OpenURL  string
	Message  string
}

func (s *Server) loaderPanel(r *http.Request, base baseData, path string) loaderPanel {
	web := s.monloaderWebBase()
	scheme := "http"
	if session.RequestIsHTTPS(r) {
		scheme = "https"
	}
	peerURL := loaderPeerURL(web, scheme, r.Host)
	if !base.MonloaderPaired {
		return loaderPanel{Message: "Pair Meloader in Settings → Plugins to use Loader."}
	}
	if peerURL == "" {
		return loaderPanel{Message: "Set a separate Meloader Web URL in Settings → Plugins. If Mebooru uses HTTPS, use HTTPS for Meloader too."}
	}
	panel := loaderPanel{OpenURL: peerURL + path}
	switch base.MonloaderConn {
	case peer.ConnPaused:
		panel.Message = "The Meloader connection is paused. Resume it from the footer or Settings → Plugins."
	case peer.ConnDown, peer.ConnRejected:
		panel.Message = "Meloader is unavailable. Check that it is running and paired, then reload this page."
	default:
		params := url.Values{"embed": {"1"}}
		if path == "/queue" {
			if base.ActiveGallery == "" || base.Degraded {
				panel.Message = "Choose an available gallery before queueing imports."
				return panel
			}
			path = "/loader/queue"
			params.Set("gallery", base.ActiveGallery)
			panel.OpenURL = peerURL + path + "?" + params.Encode()
		}
		panel.FrameURL = peerURL + path + "?" + params.Encode()
	}
	return panel
}

func loaderPeerURL(raw, scheme, host string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || (scheme == "https" && u.Scheme != "https") {
		return ""
	}
	origin := u.Scheme + "://" + u.Host
	// The frame keeps the peer's scripts and credentials on its own origin.
	if loaderSameOrigin(u, scheme, host) {
		return ""
	}
	return origin
}

func loaderSameOrigin(u *url.URL, scheme, host string) bool {
	parent, err := url.Parse(scheme + "://" + host)
	if err != nil || u.Scheme != parent.Scheme ||
		!strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), strings.TrimSuffix(parent.Hostname(), ".")) {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return port(u) == port(parent)
}

func (s *Server) loaderPage(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	path := "/queue"
	switch view {
	case "watches", "ptr":
		path = "/" + view
	default:
		view = "queue"
	}
	base := s.base(r, "loader", "Loader - "+s.booruName())
	s.renderTemplate(w, "loader.html", struct {
		baseData
		View  string
		Panel loaderPanel
	}{base, view, s.loaderPanel(r, base, path)})
}
