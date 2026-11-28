package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jwebb7847-ui/mebooru/shared/fsx"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/monbooru/monbooru/internal/web"
)

func (a *managedApplication) ReadAcquisitionSettings() (web.AcquisitionSettings, error) {
	settings := a.AcquisitionSettings()
	if settings.PolicyFile == "" {
		return settings, nil
	}
	body, err := os.ReadFile(settings.PolicyFile)
	if err != nil {
		return settings, err
	}
	var conf liveSettings
	if err := conf.decode(body); err != nil {
		return settings, err
	}
	settings.Runtime = conf.GalleryDL.Binary
	settings.SleepRequest = conf.GalleryDL.Sleep
	settings.MaxItems = conf.Downloader.Limit
	settings.Blacklist = strings.Join(conf.Tags.Blacklist, "\n")
	for source, digest := range a.owner.live.SourceOptions {
		if digest == "-" {
			settings.BlockedSources = append(settings.BlockedSources, source)
		}
	}
	slices.Sort(settings.BlockedSources)
	runtime, _ := liveRuntimePath(settings.Runtime)
	settings.NeedsRestart = runtime != a.owner.live.Runtime || settings.SleepRequest != a.owner.live.SleepRequest || settings.MaxItems != a.owner.live.MaxItems
	a.readRuntimeStatus(&settings)
	return settings, nil
}

func (a *managedApplication) SaveAcquisitionSettings(ctx context.Context, settings web.AcquisitionSettings) error {
	if a.owner.live == nil {
		return web.ErrManagedImportUnsupported
	}
	blacklist, err := validateManagedBlacklist(ctx, settings.Blacklist)
	if err != nil {
		return err
	}
	a.policyMu.Lock()
	defer a.policyMu.Unlock()

	path, err := filepath.EvalSymlinks(a.owner.live.PolicyConfig)
	if err != nil {
		return errors.New("Could not find the acquisition settings file.")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return errors.New("Could not read the acquisition settings file.")
	}
	var conf liveSettings
	var document map[string]any
	if err := conf.decode(body); err != nil {
		return errors.New("Fix the TOML syntax in the acquisition settings file before saving.")
	}
	if _, err := toml.Decode(string(body), &document); err != nil {
		return errors.New("Could not read the acquisition settings file.")
	}
	conf.GalleryDL.Binary = settings.Runtime
	conf.GalleryDL.Sleep = settings.SleepRequest
	conf.Downloader.Limit = settings.MaxItems
	if _, err := conf.bind(path); err != nil {
		return errors.New("Use the bundled gallery-dl or an absolute installed path, a nonnegative request delay, and a supported maximum post count.")
	}
	apply := func(section string, fields map[string]any) {
		current, _ := document[section].(map[string]any)
		if current == nil {
			current = make(map[string]any)
			document[section] = current
		}
		for key, value := range fields {
			current[key] = value
		}
	}
	apply("gallerydl", map[string]any{"binary_path": settings.Runtime, "sleep_request": settings.SleepRequest})
	apply("downloader", map[string]any{"max_items_per_job": settings.MaxItems})
	apply("tags", map[string]any{"strict_blacklist": true, "blacklist": blacklist})
	if err := fsx.WriteDurable(path, func(f *os.File) error {
		return toml.NewEncoder(f).Encode(document)
	}); err != nil {
		return errors.New("Could not save the acquisition settings file.")
	}
	return nil
}

func validateManagedBlacklist(ctx context.Context, text string) ([]string, error) {
	worker, err := processguard.ProbeSibling("monbooru", "monloader")
	if err != nil {
		return nil, errors.New("Could not find the installed acquisition worker.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, worker, "internal-managed-check-blacklist-v1")
	cmd.Stdin = strings.NewReader(text)
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	raw, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(diagnostic.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, errors.New("Could not validate the strict blacklist.")
	}
	var rules []string
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, errors.New("Could not read the validated strict blacklist.")
	}
	return rules, nil
}
