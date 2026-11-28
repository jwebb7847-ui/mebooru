package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

type LoaderWatch struct {
	ID            int64
	URL           string
	Gallery       string
	IntervalHours int
	MaxItems      int
	Live          bool
	LiveJob       int64
	Unavailable   bool
	Last          struct {
		CheckedAt   time.Time `json:"checked_at"`
		LastSuccess time.Time `json:"last_success"`
		JobID       int64     `json:"job_id"`
		Status      string    `json:"status"`
		Created     int       `json:"created"`
		Duplicate   int       `json:"duplicate"`
		Blacklisted int       `json:"blacklisted"`
	}
}

type managedWatches interface {
	ListWatches(context.Context) ([]LoaderWatch, error)
	AddWatch(context.Context, string, string, int, int) error
	RemoveWatch(context.Context, int64) error
	ScheduleWatch(context.Context, int64, int) error
	CheckWatch(context.Context, int64) error
	CancelWatch(context.Context, int64) error
}

type loaderWatches struct {
	Entries        []LoaderWatch
	Intervals      []int
	DefaultGallery string
	Message        string
}

func (s *Server) loaderWatches(r *http.Request) loaderWatches {
	view := loaderWatches{DefaultGallery: s.defaultGallery(), Intervals: []int{0, 6, 12, 24, 72, 168}}
	service, ok := s.managedLoader.(managedWatches)
	if !ok {
		view.Message = "Managed watches are unavailable."
		return view
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	entries, err := service.ListWatches(ctx)
	if err != nil {
		view.Message = "Could not load watched URLs."
		return view
	}
	view.Entries = entries
	return view
}

func (v *loaderWatches) markUnavailable(destinations []string, limit int) {
	for i := range v.Entries {
		watch := &v.Entries[i]
		destination := watch.Gallery
		if destination == "" {
			destination = v.DefaultGallery
		}
		watch.Unavailable = !slices.Contains(destinations, destination) || watch.MaxItems > limit || workerproto.StrictQueueSource(watch.URL) == ""
	}
}

func (s *Server) loaderWatchAction(w http.ResponseWriter, r *http.Request) {
	if s.refuseUnavailableAcquisition(w, r) {
		return
	}
	service, ok := s.managedLoader.(managedWatches)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !parseFormOK(w, r) {
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("watch_id"), 10, 64)
	hours, hoursErr := strconv.Atoi(r.FormValue("interval"))
	limit, limitErr := strconv.Atoi(r.FormValue("max_items"))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var err error
	switch action := r.FormValue("action"); {
	case action == "add" && hoursErr == nil && limitErr == nil:
		err = service.AddWatch(ctx, strings.TrimSpace(r.FormValue("url")), r.FormValue("destination"), hours, limit)
	case action == "schedule" && id > 0 && hoursErr == nil:
		err = service.ScheduleWatch(ctx, id, hours)
	case action == "check" && id > 0:
		err = service.CheckWatch(ctx, id)
	case action == "cancel" && id > 0:
		err = service.CancelWatch(ctx, id)
	case action == "remove" && id > 0:
		err = service.RemoveWatch(ctx, id)
	default:
		err = ErrManagedImportUnsupported
	}
	if err != nil {
		externalErr(w, r, "The watch could not be updated. Check the source, post limit, or queue status.", http.StatusConflict)
		return
	}
	hxRedirect(w, r, "/loader?view=watches")
}
