package workerproto

import (
	"encoding/json"
	"unicode/utf8"
)

// ResolveTags is a local, read-only query. Gallery is explicit; inputs
// retain order and duplicates so the client can verify every result.
const MaxResolveNames = 500
const MaxResolveNameBytes = 1024
const MaxResolveImplications = 8192
const MaxResolveResponseBytes = 4 << 20

type ResolveTagsRequest struct {
	Gallery string `json:"gallery"`
	Names []string `json:"names"`
}

type TagResolution struct {
	Name string `json:"name"`
	Status string `json:"status"`
	ResolvesTo string `json:"resolves_to,omitempty"`
	Implies []string `json:"implies"`
	Reason string `json:"reason,omitempty"`
}

type ResolveTagsResponse struct {
	Gallery string `json:"gallery"`
	Results []TagResolution `json:"results"`
}

// Empty/invalid tag names are legitimate queries: the library returns
// refused rather than changing or silently dropping the input.
func validResolveName(name string) bool {
	return utf8.ValidString(name) && len(name) <= MaxResolveNameBytes
}

func validResolveRequest(q ResolveTagsRequest) bool {
	if !validCatalogName(q.Gallery) || len(q.Names) == 0 || len(q.Names) > MaxResolveNames {
		return false
	}
	for _, name := range q.Names {
		if !validResolveName(name) { return false }
	}
	return true
}

func DecodeResolveTagsRequest(body []byte) (ResolveTagsRequest, error) {
	var wire struct {
		Gallery string `json:"gallery"`
		Names []*string `json:"names"`
	}
	if len(body) > 4<<20 || decodeObject(body, &wire, "gallery", "names") != nil || len(wire.Names) > MaxResolveNames {
		return ResolveTagsRequest{}, ErrMessage
	}
	q := ResolveTagsRequest{Gallery: wire.Gallery, Names: make([]string, 0, len(wire.Names))}
	for _, name := range wire.Names {
		if name == nil { return ResolveTagsRequest{}, ErrMessage }
		q.Names = append(q.Names, *name)
	}
	if !validResolveRequest(q) { return ResolveTagsRequest{}, ErrMessage }
	return q, nil
}

func EncodeResolveTagsRequest(q ResolveTagsRequest) (json.RawMessage, error) {
	if !validResolveRequest(q) { return nil, ErrMessage }
	body, err := json.Marshal(q)
	if err != nil { return nil, err }
	if _, err := DecodeResolveTagsRequest(body); err != nil { return nil, err }
	return body, nil
}

// Check exact gallery, count and ordered names, not only message shape.
// A malformed or incomplete reply must never become a partial success.
func DecodeResolveTagsResponse(body []byte, q ResolveTagsRequest) (ResolveTagsResponse, error) {
	var wire struct {
		Gallery string `json:"gallery"`
		Results []json.RawMessage `json:"results"`
	}
	if !validResolveRequest(q) || len(body) > MaxResolveResponseBytes ||
		decodeObject(body, &wire, "gallery", "results") != nil ||
		wire.Gallery != q.Gallery || len(wire.Results) != len(q.Names) {
		return ResolveTagsResponse{}, ErrMessage
	}
	out := ResolveTagsResponse{Gallery: wire.Gallery, Results: make([]TagResolution, 0, len(q.Names))}
	total := 0
	for i, item := range wire.Results {
		var r struct {
			Name *string `json:"name"`
			Status string `json:"status"`
			ResolvesTo string `json:"resolves_to"`
			Implies []*string `json:"implies"`
			Reason string `json:"reason"`
		}
		if decodeObject(item, &r, "name", "status", "resolves_to", "implies", "reason") != nil ||
			r.Name == nil || *r.Name != q.Names[i] || r.Implies == nil ||
			len(r.Implies) > MaxResolveImplications-total || !validResolveName(r.Reason) {
			return ResolveTagsResponse{}, ErrMessage
		}
		switch r.Status {
		case "alias":
			if !validCatalogName(r.ResolvesTo) || r.Reason != "" { return ResolveTagsResponse{}, ErrMessage }
		case "exists":
			if r.ResolvesTo != "" || r.Reason != "" { return ResolveTagsResponse{}, ErrMessage }
		case "new", "rejected":
			if r.ResolvesTo != "" || r.Reason != "" || len(r.Implies) != 0 { return ResolveTagsResponse{}, ErrMessage }
		case "refused":
			if r.ResolvesTo != "" || !validCatalogName(r.Reason) || len(r.Implies) != 0 { return ResolveTagsResponse{}, ErrMessage }
		default:
			return ResolveTagsResponse{}, ErrMessage
		}
		row := TagResolution{Name: *r.Name, Status: r.Status, ResolvesTo: r.ResolvesTo, Reason: r.Reason, Implies: make([]string, 0, len(r.Implies))}
		for _, name := range r.Implies {
			if name == nil || !validCatalogName(*name) { return ResolveTagsResponse{}, ErrMessage }
			row.Implies = append(row.Implies, *name)
		}
		total += len(row.Implies)
		out.Results = append(out.Results, row)
	}
	return out, nil
}

func EncodeResolveTagsResponse(out ResolveTagsResponse, q ResolveTagsRequest) (json.RawMessage, error) {
	if !validResolveRequest(q) || out.Gallery != q.Gallery || len(out.Results) != len(q.Names) {
		return nil, ErrMessage
	}
	total := 0
	for _, r := range out.Results {
		if len(r.Implies) > MaxResolveImplications-total || !validResolveName(r.Name) ||
			!validResolveName(r.ResolvesTo) || !validResolveName(r.Reason) || len(r.Status) > 16 {
			return nil, ErrMessage
		}
		total += len(r.Implies)
		for _, name := range r.Implies {
			if !validResolveName(name) { return nil, ErrMessage }
		}
	}
	body, err := json.Marshal(out)
	if err != nil { return nil, err }
	if _, err := DecodeResolveTagsResponse(body, q); err != nil { return nil, err }
	return body, nil
}
