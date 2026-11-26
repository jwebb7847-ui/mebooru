package workerproto

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Initial strict metadata contract. A request is not an authorization.
// Custody must independently bind it to a trusted job, gallery, runtime,
// config generation, and online admission before any execution.
type MetadataKind string

const (
	MetadataClassify MetadataKind = "classify"
	MetadataStrictResolve MetadataKind = "strict_resolve"
	MaxMetadataURLBytes = 4096
	MaxMetadataWindow = 1001
	MaxMetadataPosition = 2147483647
)

type MetadataPlanRequest struct {
	JobID int64 `json:"job_id"`
	Gallery string `json:"gallery"`
	Kind MetadataKind `json:"kind"`
	Source string `json:"source"`
	URL string `json:"url"`
	First uint32 `json:"first,omitempty"`
	Last uint32 `json:"last,omitempty"`
}

// Restrict the initial plan family to exact qualified source hosts.
// This is input validation, not DNS/redirect confinement or a guarantee
// of source identity. Custom hosts remain legacy-only until qualified.
func ValidMetadataPlan(q MetadataPlanRequest) bool {
	if q.JobID <= 0 || !validCatalogName(q.Gallery) ||
		len(q.URL) == 0 || len(q.URL) > MaxMetadataURLBytes || !utf8.ValidString(q.URL) ||
		strings.ContainsFunc(q.URL, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return false
	}
	var host string
	switch q.Source {
	case "derpibooru": host = "derpibooru.org"
	case "danbooru": host = "danbooru.donmai.us"
	case "e621": host = "e621.net"
	default: return false
	}
	u, err := url.Parse(q.URL)
	if err != nil || u.Scheme != "https" || u.Host != host ||
		u.User != nil || u.Opaque != "" || u.Fragment != "" ||
		strings.Contains(q.URL, "#") || u.Path == "" || strings.Contains(q.URL, "\\") {
		return false
	}
	switch q.Kind {
	case MetadataClassify:
		return q.First == 0 && q.Last == 0
	case MetadataStrictResolve:
		return q.First > 0 && q.Last >= q.First && q.Last <= MaxMetadataPosition &&
			q.Last-q.First < MaxMetadataWindow
	default:
		return false
	}
}

func DecodeMetadataPlanRequest(body []byte) (MetadataPlanRequest, error) {
	var wire struct {
		JobID int64 `json:"job_id"`
		Gallery string `json:"gallery"`
		Kind MetadataKind `json:"kind"`
		Source string `json:"source"`
		URL string `json:"url"`
		First *uint32 `json:"first"`
		Last *uint32 `json:"last"`
	}
	if len(body) > 32<<10 || decodeObject(body, &wire, "job_id", "gallery", "kind", "source", "url", "first", "last") != nil {
		return MetadataPlanRequest{}, ErrMessage
	}
	q := MetadataPlanRequest{JobID:wire.JobID, Gallery:wire.Gallery, Kind:wire.Kind, Source:wire.Source, URL:wire.URL}
	switch q.Kind {
	case MetadataClassify:
		if wire.First != nil || wire.Last != nil { return MetadataPlanRequest{}, ErrMessage }
	case MetadataStrictResolve:
		if wire.First == nil || wire.Last == nil { return MetadataPlanRequest{}, ErrMessage }
		q.First, q.Last = *wire.First, *wire.Last
	}
	if !ValidMetadataPlan(q) { return MetadataPlanRequest{}, ErrMessage }
	return q, nil
}

func EncodeMetadataPlanRequest(q MetadataPlanRequest) (json.RawMessage, error) {
	if !ValidMetadataPlan(q) { return nil, ErrMessage }
	body, err := json.Marshal(q)
	if err != nil { return nil, err }
	if _, err := DecodeMetadataPlanRequest(body); err != nil { return nil, err }
	return body, nil
}
