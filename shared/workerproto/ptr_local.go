package workerproto

import "encoding/hex"

// PTRLocalTagsRequest reads an existing worker-owned index. An empty
// SHA256 requests its availability. There is no network fallback.
type PTRLocalTagsRequest struct {
	SHA256 string `json:"sha256"`
}

type PTRLocalTags struct {
	Enabled   bool     `json:"enabled"`
	Indexed   bool     `json:"indexed"`
	Found     bool     `json:"found"`
	Tags      []string `json:"tags,omitempty"`
	SyncState string   `json:"sync_state,omitempty"`
}

type PTRSyncRequest struct {
	Run bool `json:"run"`
}

func DecodePTRSyncRequest(body []byte) (PTRSyncRequest, error) {
	var value struct {
		Run *bool `json:"run"`
	}
	if err := decodeObject(body, &value, "run"); err != nil || value.Run == nil {
		return PTRSyncRequest{}, ErrMessage
	}
	return PTRSyncRequest{Run: *value.Run}, nil
}

func ValidPTRHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	raw, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(raw) == s
}

func DecodePTRLocalTagsRequest(body []byte) (PTRLocalTagsRequest, error) {
	var req PTRLocalTagsRequest
	if err := decodeObject(body, &req, "sha256"); err != nil || (req.SHA256 != "" && !ValidPTRHash(req.SHA256)) {
		return PTRLocalTagsRequest{}, ErrMessage
	}
	return req, nil
}
