package web

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/apitoken"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/monloader"
)

func (s *Server) OfferLegacyMigration(prepare func(context.Context, string, string, string) (string, error)) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.legacyMigration = prepare
	s.DisableManagedLoader()
}

func withoutLegacyAcquisitionTokens(tokens apitoken.List) apitoken.List {
	return slices.DeleteFunc(tokens, func(t apitoken.Token) bool { return t.Paired == monloader.App })
}

func (s *Server) migrateLegacyAcquisition(w http.ResponseWriter, r *http.Request) {
	if s.legacyMigration == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if !parseFormOK(w, r) {
		return
	}
	if r.FormValue("confirmed") != "on" {
		externalErr(w, r, "Quit the old worker and confirm it is stopped.", http.StatusBadRequest)
		return
	}
	if err := s.withConfig(func(cfg *config.Config) error {
		if cfg.Desktop.ManagedAcquisition {
			return ErrManagedImportUnsupported
		}
		path, err := s.legacyMigration(r.Context(), strings.TrimSpace(r.FormValue("config")), strings.TrimSpace(r.FormValue("state")), cfg.DefaultGallery)
		if err != nil {
			return err
		}
		cfg.Desktop.ManagedAcquisition = true
		cfg.Desktop.MeloaderBinary = ""
		cfg.Desktop.MeloaderConfig = path
		cfg.Monloader = config.MonloaderConfig{}
		cfg.Auth.Tokens = withoutLegacyAcquisitionTokens(cfg.Auth.Tokens)
		return nil
	}); err != nil {
		externalErr(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	s.settingsRestart(w, r)
}
