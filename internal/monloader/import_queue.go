package monloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
)

const importQueuePath = "/api/v1/queue"

var ErrImportJobNotFound = errors.New("import job not found")

type ImportQueuePage struct {
	Page   int         `json:"page"`
	Limit  int         `json:"limit"`
	Total  int         `json:"total"`
	Paused bool        `json:"paused"`
	Jobs   []ImportJob `json:"jobs"`
}

type ImportJob struct {
	ID, Root int64
	URL      string
	Kind     string
	MD5      string
	Site     string
	Gallery  string
	Status   string
	Error    string
	Capped   bool
	MaxItems int `json:"max_items"`
	Summary  struct {
		Created, Duplicate, Skipped, Blacklisted, Failed, Total int
	}
}

type ImportItem struct {
	PostID    string `json:"post_id"`
	Num       int    `json:"num"`
	Status    string `json:"status"`
	Outcome   string `json:"outcome"`
	SHA256    string `json:"sha256"`
	ErrorCode string `json:"error_code"`
	Error     string `json:"error"`
}

type ImportJobResults struct {
	ImportJob
	Items []ImportItem `json:"items"`
}

func (l *Link) ImportResults(ctx context.Context, id int64) (ImportJobResults, error) {
	var out ImportJobResults
	resp, err := l.client.Do(ctx, http.MethodGet, importQueuePath+"/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return out, ErrImportJobNotFound
	case http.StatusOK:
		err = json.NewDecoder(resp.Body).Decode(&out)
		return out, err
	default:
		return out, refused(resp)
	}
}

func (j ImportJob) Live() bool {
	return j.Status == contract.JobQueued || j.Status == contract.JobRunning
}

func (j ImportJob) Series() int64 {
	if j.Root != 0 {
		return j.Root
	}
	return j.ID
}

func (l *Link) ImportQueue(ctx context.Context, page, limit int) (ImportQueuePage, error) {
	var out ImportQueuePage
	path := fmt.Sprintf("%s?page=%d&limit=%d", importQueuePath, page, limit)
	resp, err := l.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, refused(resp)
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

type importOptions struct {
	Gallery  string `json:"gallery"`
	MaxItems int    `json:"max_items,omitempty"`
}

func (l *Link) SubmitImport(ctx context.Context, url, gallery string, maxItems int) (int64, error) {
	return l.enqueue(ctx, importQueuePath, struct {
		URL     string        `json:"url"`
		Options importOptions `json:"options"`
	}{url, importOptions{Gallery: gallery, MaxItems: maxItems}}, nil)
}

// AdvanceImport performs a user-requested operation on Meloader's job.
func (l *Link) AdvanceImport(ctx context.Context, id int64, retry bool) (int64, error) {
	action := "/continue"
	if retry {
		action = "/retry"
	}
	return l.enqueue(ctx, importQueuePath+"/"+strconv.FormatInt(id, 10)+action, nil, nil)
}
