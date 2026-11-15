package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/monbooru/monbooru/internal/monloader"
	"github.com/monbooru/monbooru/internal/shared/contract"
)

const loaderQueueLimit = 20

func (s *Server) loaderJobs(w http.ResponseWriter, r *http.Request) {
	page, _ := contract.PageParams(r.URL.Query().Get("page"), "", loaderQueueLimit, loaderQueueLimit)
	view := struct {
		monloader.ImportQueuePage
		Message    string
		Prev, Next int
		Latest     map[int64]int64
	}{}
	w.Header().Set("Cache-Control", "no-store")
	link := s.monloaderLink()
	switch {
	case !link.Paired:
		view.Message = "Pair Meloader in Settings → Plugins to see import jobs."
	case link.Paused:
		view.Message = "Meloader is paused. Resume it from the footer or Settings → Plugins."
	default:
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		out, err := s.ml.ImportQueue(ctx, page, loaderQueueLimit)
		if err != nil {
			view.Message = "Queue unavailable. Check the Meloader connection in Settings → Plugins."
		} else {
			view.ImportQueuePage = out
			view.Latest = make(map[int64]int64, len(out.Jobs))
			for _, job := range out.Jobs {
				series := job.Series()
				view.Latest[series] = max(view.Latest[series], job.ID)
			}
			if page > 1 {
				view.Prev = page - 1
			}
			if out.Total > 0 && page <= (out.Total-1)/loaderQueueLimit {
				view.Next = page + 1
			}
		}
	}
	s.renderTemplate(w, "loader_jobs", view)
}

func (s *Server) loaderImport(w http.ResponseWriter, r *http.Request) {
	if !parseFormOK(w, r) {
		return
	}
	// Check the gallery advertised by this form. Release the gallery lock
	// before waiting on the peer, and send the captured name explicitly.
	s.set.RLock()
	cx := s.active()
	name := s.activeGallery()
	available := cx != nil && !cx.Degraded && name != ""
	s.set.RUnlock()
	if r.FormValue("_gallery") != name || !available {
		externalErr(w, r, "The destination gallery changed or is unavailable. Reload Loader.", http.StatusConflict)
		return
	}
	source := strings.TrimSpace(r.FormValue("url"))
	u, err := url.Parse(source)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		externalErr(w, r, "Enter a valid HTTP or HTTPS source URL.", http.StatusBadRequest)
		return
	}
	var maxItems int
	if value := r.FormValue("max_items"); value != "" {
		maxItems, err = strconv.Atoi(value)
		if err != nil || maxItems < 1 {
			externalErr(w, r, "Enter a positive post limit or leave it blank.", http.StatusBadRequest)
			return
		}
	}
	if !s.monloaderLink().Paired || s.ml.Paused() {
		externalErr(w, r, "Connect Meloader in Settings → Plugins before queueing imports.", http.StatusConflict)
		return
	}
	id, err := s.ml.SubmitImport(r.Context(), source, name, maxItems)
	writeLoaderQueueResult(w, r, id, err)
}

func (s *Server) loaderJobAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	action := r.PathValue("action")
	if err != nil || id <= 0 || (action != "retry" && action != "continue") {
		http.NotFound(w, r)
		return
	}
	if !s.monloaderLink().Paired || s.ml.Paused() {
		externalErr(w, r, "Connect Meloader before updating the queue.", http.StatusConflict)
		return
	}
	job, err := s.ml.AdvanceImport(r.Context(), id, action == "retry")
	writeLoaderQueueResult(w, r, job, err)
}

func writeLoaderQueueResult(w http.ResponseWriter, r *http.Request, id int64, err error) {
	if err != nil || id <= 0 {
		externalErr(w, r, "Meloader did not confirm the request. Check the queue before trying again.", http.StatusBadGateway)
		return
	}
	if !isHTMXRequest(r) {
		http.Redirect(w, r, "/loader", http.StatusSeeOther)
		return
	}
	w.Header().Set("HX-Trigger", "loader-queue-updated")
	writeInlineFlash(w, "ok", fmt.Sprintf("Job %d queued.", id))
}
