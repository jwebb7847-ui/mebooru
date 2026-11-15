package web

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/monloader"
	"github.com/monbooru/monbooru/internal/shared/contract"
)

const loaderQueueLimit = 20
const loaderResultsLimit = 50

type loaderResult struct {
	PostID string
	Number int
	Label  string
	Detail string
	URL    string
}

func (s *Server) loaderJobResults(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	view := struct {
		ID                     int64
		Message                string
		Rows                   []loaderResult
		First, Last, Total     int
		Page                   int
		PreviousPage, NextPage int
	}{ID: id}
	view.Page, _ = contract.PageParams(r.URL.Query().Get("page"), "", loaderResultsLimit, loaderResultsLimit)

	link := s.monloaderLink()
	switch {
	case !link.Paired:
		view.Message = "Pair Meloader to see import results."
	case link.Paused:
		view.Message = "Resume Meloader to see import results."
	default:
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		gallery, items, err := s.ml.ImportResults(ctx, id)
		switch {
		case errors.Is(err, monloader.ErrImportJobNotFound):
			view.Message = "This job is no longer in the queue."
		case err != nil:
			view.Message = "Could not load import results."
		case len(items) == 0:
			view.Message = "No post results yet."
		default:
			view.Total = len(items)
			lastPage := (view.Total-1)/loaderResultsLimit + 1
			page := min(view.Page, lastPage)
			view.Page = page
			start := (page - 1) * loaderResultsLimit
			view.First, view.Last = start+1, min(start+loaderResultsLimit, view.Total)
			for index, item := range items[start:view.Last] {
				view.Rows = append(view.Rows, buildLoaderResult(gallery, item, start+index+1))
			}
			if page > 1 {
				view.PreviousPage = page - 1
			}
			if page < lastPage {
				view.NextPage = page + 1
			}
		}
	}
	s.renderTemplate(w, "loader_job_results", view)
}

func buildLoaderResult(gallery string, item monloader.ImportItem, number int) loaderResult {
	row := loaderResult{PostID: item.PostID, Number: cmp.Or(item.Num, number), Detail: item.Error}
	outcome := cmp.Or(item.Outcome, item.Status)
	switch {
	case item.ErrorCode == contract.CodeCanceled:
		row.Label = "Canceled"
	case outcome == contract.OutcomeSkippedBlacklist:
		row.Label = "Blacklisted"
	case outcome == contract.OutcomeSkippedArchive:
		row.Label = "Skipped by archive"
	case outcome == contract.OutcomeSkippedUnsupported:
		row.Label = "Skipped (unsupported file type)"
	default:
		row.Label = strings.ReplaceAll(cmp.Or(outcome, "pending"), "_", " ")
	}
	switch item.Outcome {
	case contract.OutcomeCreated, contract.OutcomeDuplicate, contract.OutcomeEnriched, contract.OutcomeReplaced:
		if config.ValidateGalleryName(gallery) == nil && len(item.SHA256) == 2*sha256.Size {
			if _, err := hex.DecodeString(item.SHA256); err == nil {
				row.URL = "/i/" + strings.ToLower(item.SHA256) + "?gallery=" + url.QueryEscape(gallery)
			}
		}
	}
	return row
}

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
