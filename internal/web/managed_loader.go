package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/desktop"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"

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

type AcquisitionSettings struct {
	Runtime, PolicyFile string
	Blacklist           string
	SleepRequest        float64
	MaxItems            int
	NeedsRestart        bool
}

type managedSettingsProvider interface {
	AcquisitionSettings() AcquisitionSettings
}

type managedSettingsEditor interface {
	ReadAcquisitionSettings() (AcquisitionSettings, error)
	SaveAcquisitionSettings(context.Context, AcquisitionSettings) error
}

var ErrManagedImportUnsupported = errors.New("unsupported managed import")

const acquisitionUnavailable = "Acquisition is unavailable. See the application log for repair details. Your local library is available offline."

// DisableManagedLoader is used before serving HTTP when acquisition
// cannot establish safe staging or ownership. It keeps legacy pairing off.
func (s *Server) DisableManagedLoader() {
	s.loaderUnavailable = true
}

func (s *Server) usesManagedLoader() bool {
	return s.managedLoader != nil || s.loaderUnavailable
}

func (s *Server) acquisitionUnavailableMessage() string {
	if s.loaderUnavailable {
		return acquisitionUnavailable
	}
	return ""
}

func (s *Server) refuseUnavailableAcquisition(w http.ResponseWriter, r *http.Request) bool {
	if !s.loaderUnavailable {
		return false
	}
	externalErr(w, r, acquisitionUnavailable, http.StatusServiceUnavailable)
	return true
}

func (s *Server) saveAcquisitionSettings(w http.ResponseWriter, r *http.Request) {
	if s.refuseUnavailableAcquisition(w, r) {
		return
	}
	editor, ok := s.managedLoader.(managedSettingsEditor)
	if !ok || !desktop.LocalRequest(r) {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if !parseFormOK(w, r) {
		return
	}
	settings := AcquisitionSettings{Runtime: strings.TrimSpace(r.FormValue("runtime")), Blacklist: r.FormValue("blacklist")}
	var err error
	settings.MaxItems, err = strconv.Atoi(r.FormValue("max_items"))
	if err != nil || settings.MaxItems < 1 || settings.MaxItems > workerproto.MaxMetadataWindow {
		externalErr(w, r, "Enter a maximum post count within the displayed range.", http.StatusBadRequest)
		return
	}
	settings.SleepRequest, err = strconv.ParseFloat(r.FormValue("sleep_request"), 64)
	if err != nil {
		externalErr(w, r, "Enter a request delay in seconds.", http.StatusBadRequest)
		return
	}
	if err := editor.SaveAcquisitionSettings(r.Context(), settings); err != nil {
		externalErr(w, r, err.Error(), http.StatusBadRequest)
		return
	}
	hxRedirect(w, r, "/settings?acquisition=saved#meloader")
}

func (s *Server) loaderOffline(w http.ResponseWriter, r *http.Request) {
	if s.refuseUnavailableAcquisition(w, r) {
		return
	}
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
	target := "/loader"
	if r.FormValue("return_to") == "settings" {
		target = "/settings#meloader"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
