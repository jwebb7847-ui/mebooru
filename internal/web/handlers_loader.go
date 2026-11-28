package web

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/desktop"
	"github.com/jwebb7847-ui/mebooru/shared/session"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/peer"
)

type loaderPanel struct {
	FrameURL string
	OpenURL  string
	Message  string
	Setup    bool
}

type localPTRLoader interface {
	LocalPTRTags(context.Context, string) (workerproto.PTRLocalTags, error)
}

type loaderPTR struct {
	Query, Message string
	workerproto.PTRLocalTags
}

func (s *Server) loaderLocalPTR(r *http.Request) loaderPTR {
	hash := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sha256")))
	if hash != "" && !workerproto.ValidPTRHash(hash) {
		return loaderPTR{Message: "Enter a 64-character SHA-256 hash."}
	}
	reader, _ := s.managedLoader.(localPTRLoader)
	if reader == nil {
		return loaderPTR{Message: "The local PTR index is unavailable."}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := reader.LocalPTRTags(ctx, hash)
	if err != nil {
		return loaderPTR{Query: hash, Message: "Could not read the local PTR index."}
	}
	return loaderPTR{Query: hash, PTRLocalTags: result}
}

func (s *Server) loaderPanel(r *http.Request, base baseData, path string) loaderPanel {
	if !base.MonloaderPaired {
		return loaderPanel{Message: "Pair Meloader to use this view.", Setup: true}
	}
	switch base.MonloaderConn {
	case peer.ConnPaused:
		return loaderPanel{Message: "The Meloader connection is paused. Resume it from the footer or Settings → Plugins.", Setup: true}
	case peer.ConnDown:
		return loaderPanel{Message: "Meloader is unreachable. Start the companion, then reload this page."}
	case peer.ConnRejected:
		return loaderPanel{Message: "Meloader rejected the connection. Check the pairing in Settings → Plugins.", Setup: true}
	}
	web := s.monloaderWebBase()
	scheme := "http"
	if session.RequestIsHTTPS(r) {
		scheme = "https"
	}
	// A paired address on the same machine can serve both the API and the
	// browser. Keep remote and proxied installs on their explicit web URL.
	if web == "" && scheme == "http" && desktop.LocalRequest(r) {
		link := s.monloaderLink()
		if link.Paired {
			candidate := cmp.Or(strings.TrimSpace(link.APIURL), link.PeerURL)
			if u, err := url.Parse(candidate); err == nil && desktop.IsLoopbackAddr(u.Hostname()) {
				web = candidate
			}
		}
	}
	peerURL := loaderPeerURL(web, scheme, r.Host)
	if peerURL == "" {
		return loaderPanel{Message: "Set Meloader’s Web URL in Settings → Plugins. Use HTTPS for both apps if Mebooru uses HTTPS.", Setup: true}
	}
	panel := loaderPanel{OpenURL: peerURL + path}
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
	var path string
	switch view {
	case "watches", "ptr":
		path = "/" + view
	case "advanced":
		path = "/queue"
	default:
		view = "queue"
	}
	base := s.base(r, "loader", "Loader - "+s.booruName())
	if s.loaderUnavailable {
		view, path = "queue", ""
	}
	var panel loaderPanel
	var ptr loaderPTR
	if path != "" {
		if view == "ptr" && s.managedLoader != nil {
			ptr = s.loaderLocalPTR(r)
		} else if s.managedLoader != nil {
			panel.Message = "This view is unavailable with the managed queue."
		} else {
			panel = s.loaderPanel(r, base, path)
		}
	}
	managed := s.managedLoader != nil
	var destinations []string
	var limit int
	if managed {
		s.set.RLock()
		for _, gallery := range s.set.All() {
			if !gallery.Degraded {
				destinations = append(destinations, gallery.Name)
			}
		}
		s.set.RUnlock()
		slices.Sort(destinations)
		if provider, ok := s.managedLoader.(managedSettingsProvider); ok {
			limit = provider.AcquisitionSettings().MaxItems
		}
	}
	s.renderTemplate(w, "loader.html", struct {
		baseData
		View         string
		Panel        loaderPanel
		Managed      bool
		Offline      bool
		Destinations []string
		Limit        int
		Unavailable  string
		PTR          loaderPTR
	}{base, view, panel, s.usesManagedLoader(), managed && s.managedLoader.Offline(), destinations, limit, s.acquisitionUnavailableMessage(), ptr})
}
