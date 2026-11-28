package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/desktop"
	"github.com/jwebb7847-ui/mebooru/shared/fsx"
	"github.com/jwebb7847-ui/mebooru/shared/managedqa"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

type liveSettings struct {
	GalleryDL struct {
		Binary string  `toml:"binary_path"`
		Sleep  float64 `toml:"sleep_request"`
		Raw    string  `toml:"raw_config"`
	} `toml:"gallerydl"`
	Downloader struct {
		Limit  int    `toml:"max_items_per_job"`
		Folder string `toml:"default_folder"`
	} `toml:"downloader"`
	Tags struct {
		Blacklist []string `toml:"blacklist"`
	} `toml:"tags"`
	Sites []struct {
		Username, Password, Cookies, Options string
		Agent                                string `toml:"user_agent"`
		APIKey                               string `toml:"api_key"`
		UserID                               string `toml:"user_id"`
	} `toml:"sites"`
}

func (conf *liveSettings) decode(body []byte) error {
	conf.GalleryDL.Sleep = 1
	conf.Downloader.Limit = 200
	_, err := toml.Decode(string(body), conf)
	return err
}

func readLiveConfig(path string) (windowregistry.LiveConfig, error) {
	var out windowregistry.LiveConfig
	if path == "" {
		return out, errors.New("set desktop.meloader_config to the acquisition settings file")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return out, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	var conf liveSettings
	if err := conf.decode(body); err != nil {
		return out, err
	}
	return conf.bind(path)
}

func seedBundledPolicy(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString("[tags]\nstrict_blacklist = true\nblacklist = [\"violence\"]\n")
	return errors.Join(err, f.Close())
}

func (conf *liveSettings) bind(path string) (windowregistry.LiveConfig, error) {
	var out windowregistry.LiveConfig
	if strings.TrimSpace(conf.GalleryDL.Raw) != "" || conf.Downloader.Folder != "" {
		return out, errors.New("managed acquisition supports generated gallery-dl options and Mebooru gallery naming settings")
	}
	for _, site := range conf.Sites {
		if site.Username != "" || site.Password != "" || site.Cookies != "" || site.Options != "" || site.Agent != "" || site.APIKey != "" || site.UserID != "" {
			return out, errors.New("managed acquisition does not yet support site credentials or custom extractor options")
		}
	}
	if conf.GalleryDL.Sleep < 0 || math.IsInf(conf.GalleryDL.Sleep, 0) || math.IsNaN(conf.GalleryDL.Sleep) ||
		conf.Downloader.Limit < 1 || conf.Downloader.Limit > workerproto.MaxMetadataWindow {
		return out, errors.New("set a nonnegative gallery-dl sleep_request and a bounded max_items_per_job in acquisition settings")
	}
	runtime, err := liveRuntimePath(conf.GalleryDL.Binary)
	if err != nil {
		return out, err
	}
	hash, err := windowregistry.RuntimeDigest(runtime)
	if err != nil {
		return out, fmt.Errorf("gallery-dl runtime: %w", err)
	}
	return windowregistry.LiveConfig{Runtime: runtime, RuntimeSHA256: hash, PolicyConfig: path, SleepRequest: conf.GalleryDL.Sleep, MaxItems: conf.Downloader.Limit}, nil
}

func liveRuntimePath(configured string) (string, error) {
	if configured == "" || configured == "gallery-dl" {
		configured = desktop.BundledTool("gallery-dl")
		if configured == "" {
			configured = macRuntimeLauncher()
			if configured == "" {
				return "", errors.New("no bundled gallery-dl executable; set an absolute executable path in acquisition settings")
			}
		}
	}
	if !filepath.IsAbs(configured) {
		return "", errors.New("the gallery-dl executable path must be absolute")
	}
	return filepath.EvalSymlinks(configured)
}

func macRuntimeLauncher() string {
	dir := desktop.ExeDir()
	if filepath.Base(dir) != "MacOS" || filepath.Base(filepath.Dir(dir)) != "Contents" {
		return ""
	}
	p := filepath.Join(dir, "..", "Resources", "runtime", "gallery-dl")
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return p
}

func prepareLiveDirectory(configPath string) (string, error) {
	base, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "managed-acquisition")
	for _, path := range []string{dir, filepath.Join(dir, "stage"), filepath.Join(dir, "queue"), filepath.Join(dir, "windows")} {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return "", windowregistry.ErrWindow
		}
	}
	return dir, nil
}

func saveLiveConfig(dir string, cfg windowregistry.LiveConfig) error {
	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return fsx.WriteDurableIn(root, windowregistry.LiveConfigName, func(f *os.File) error { _, err := f.Write(body); return err })
}

func (o *managedWindowOwner) livePolicy() (windowregistry.LivePolicy, []workerproto.MetadataPost, error) {
	var pol windowregistry.LivePolicy
	q := o.window.Request
	if err := windowregistry.ReadArtifact(o.dir, windowregistry.ArtifactName(q.JobID, "policy"), &pol, windowregistry.MaxLivePolicy); err != nil {
		return pol, nil, err
	}
	raw, err := windowregistry.ReadBytes(o.dir, windowregistry.ArtifactName(q.JobID, "native"), workerproto.MaxMetadataOutputBytes)
	if err != nil || pol.Request != q || managedqa.Digest(raw) != pol.NativeSHA256 {
		return pol, nil, windowregistry.ErrWindow
	}
	posts, err := workerproto.DecodeMetadataEvidence(raw, q, pol.Subcategory)
	return pol, posts, err
}

func (o *managedWindowOwner) authorizeLiveProjection(c context.Context, p workerproto.WindowAllocateProposal) ([]byte, error) {
	if !o.current(c) || p.Transfer.Metadata != o.window.Request {
		return nil, windowregistry.ErrWindow
	}
	pol, posts, err := o.livePolicy()
	if err != nil || p.Transfer != pol.Proposal.Transfer || p.PolicySHA256 != pol.Proposal.PolicySHA256 || !bytes.Equal(p.Projection, pol.Proposal.Projection) {
		return nil, windowregistry.ErrWindow
	}
	blocked := make(map[string]bool, len(pol.Blocked))
	for _, id := range pol.Blocked {
		blocked[id] = true
	}
	ids := make([]string, 0, len(posts))
	for _, post := range posts {
		if !blocked[post.ID] {
			ids = append(ids, post.ID)
		}
	}
	cfg, id := o.live.Identity(p.Transfer.Metadata.Source)
	plan, err := acquisitionplan.NewStrictTransferPlan(p.Transfer.Metadata, pol.Subcategory, posts, ids, 1, id, cfg)
	if err != nil || plan.Request() != p.Transfer {
		return nil, windowregistry.ErrWindow
	}
	manifest, err := plan.Consume(p.Transfer, id, 1)
	if err != nil {
		return nil, err
	}
	if _, err := acquisitionplan.DecodeApprovedPolicy(p.Projection, p.Transfer, p.PolicySHA256, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}
