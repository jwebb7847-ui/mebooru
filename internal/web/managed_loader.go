package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/monbooru/monbooru/internal/monloader"
)

// ManagedLoader is the library's local queue and network-policy interface.
type ManagedLoader interface {
	ImportQueue(context.Context, int, int) (monloader.ImportQueuePage, error)
	ImportResults(context.Context, int64) (monloader.ImportJobResults, error)
	SubmitImport(context.Context, string, string, int) (int64, error)
	Cancel(context.Context, int64) error
	Offline() bool
	SetOffline(context.Context, bool) error
}

type managedRetry interface {
	CanRetry() bool
	RetryImport(context.Context, int64) (int64, error)
}

var ErrManagedImportUnsupported = errors.New("unsupported managed import")

func (s *Server) loaderOffline(w http.ResponseWriter, r *http.Request) {
	if s.managedLoader == nil {
		http.NotFound(w, r)
		return
	}
	if !parseFormOK(w, r) {
		return
	}
	requested := r.FormValue("offline")
	if requested != "true" && requested != "false" {
		http.Error(w, "Invalid acquisition network setting.", http.StatusBadRequest)
		return
	}
	if err := s.managedLoader.SetOffline(r.Context(), requested == "true"); err != nil {
		externalErr(w, r, "Could not change acquisition network access.", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/loader", http.StatusSeeOther)
}
