package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/pipeline"
)

func runManagedPolicy(args []string) error {
	if len(args) != 2 {
		return windowregistry.ErrWindow
	}
	dir := args[0]
	live, err := windowregistry.LoadLive(dir)
	if err != nil {
		return err
	}
	var cmd windowregistry.RuntimeCommand
	raw, err := base64.RawURLEncoding.DecodeString(args[1])
	if err != nil || len(raw) > 48<<10 || json.Unmarshal(raw, &cmd) != nil {
		return windowregistry.ErrWindow
	}
	reg, err := windowregistry.Open(filepath.Join(dir, "windows"))
	if err != nil {
		return err
	}
	defer reg.Close()
	grant, err := reg.Observe(cmd.Request, cmd.Generation)
	if err != nil || !reg.Admitting(grant) {
		return windowregistry.ErrWindow
	}
	online := func() bool { return windowregistry.LiveOnline(dir) }
	if !online() {
		return workerproto.ErrOffline
	}
	if _, err := os.Stat(live.PolicyConfig); err != nil {
		return err
	}
	cfg, err := config.LoadFromFileIn(live.PolicyConfig, filepath.Dir(live.PolicyConfig))
	if err != nil {
		return err
	}
	cfg.Tags.StrictBlacklist = true
	mapper, err := mapping.New(config.NewProvider(cfg), filepath.Join(filepath.Dir(live.PolicyConfig), "profiles"))
	if err != nil {
		return err
	}
	native, err := io.ReadAll(io.LimitReader(os.Stdin, workerproto.MaxMetadataOutputBytes+1))
	if err != nil || len(native) > workerproto.MaxMetadataOutputBytes {
		return windowregistry.ErrWindow
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	policy, err := pipeline.EvaluateContainedPolicy(ctx, cmd.Request, cmd.Subcategory, native, mapper, transport, live, online)
	if err != nil {
		return err
	}
	return windowregistry.WriteArtifact(dir, windowregistry.ArtifactName(cmd.Request.JobID, "policy"), policy, windowregistry.MaxLivePolicy)
}
