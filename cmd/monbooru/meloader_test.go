package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestBundledMeloaderConfigSeedsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monloader.toml")
	if err := seedBundledMeloader(path, "127.0.0.1:18455", "Audit_A"); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		SetupDone bool `toml:"setup_done"`
		Server    struct {
			Bind string `toml:"bind_address"`
		}
		Monbooru struct {
			API     string `toml:"api_url"`
			Web     string `toml:"web_url"`
			Gallery string `toml:"default_gallery"`
		}
		GalleryDL struct {
			Archive string `toml:"archive_path"`
		}
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.SetupDone || cfg.Server.Bind != "127.0.0.1:8456" || cfg.Monbooru.API != "http://127.0.0.1:18455" || cfg.Monbooru.Web != cfg.Monbooru.API || cfg.Monbooru.Gallery != "Audit_A" || !strings.HasSuffix(cfg.GalleryDL.Archive, "gallery-dl-archive.sqlite") {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	const existing = "# existing install"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedBundledMeloader(path, "127.0.0.1:8455", "default"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != existing {
		t.Fatalf("existing config changed: %q, %v", got, err)
	}
}
