package main

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/ptr"
)

func managedPTRTags(ctx context.Context, configPath string, raw json.RawMessage) (json.RawMessage, *workerproto.Fault) {
	req, err := workerproto.DecodePTRLocalTagsRequest(raw)
	if err != nil {
		return nil, &workerproto.Fault{Code: "invalid"}
	}
	cfg, err := config.LoadFromFileIn(configPath, filepath.Dir(configPath))
	if err != nil {
		return nil, &workerproto.Fault{Code: "ptr_index_unavailable"}
	}
	result, err := ptr.ReadLocalTags(ctx, cfg.PTR, req.SHA256)
	if err != nil {
		return nil, &workerproto.Fault{Code: "ptr_index_unavailable"}
	}
	body, err := json.Marshal(result)
	if err != nil || len(body) > 256<<10 {
		return nil, &workerproto.Fault{Code: "ptr_lookup_too_large"}
	}
	return body, nil
}
