package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaimNewProfileDirectoryOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-parent", "profile")
	fresh, err := claimNewProfileDir(path)
	if err != nil || !fresh {
		t.Fatalf("new profile: fresh=%v err=%v", fresh, err)
	}
	fresh, err = claimNewProfileDir(path)
	if err != nil || fresh {
		t.Fatalf("existing empty profile: fresh=%v err=%v", fresh, err)
	}
	if err := os.WriteFile(filepath.Join(path, "old-state.sqlite"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err = claimNewProfileDir(path)
	if err != nil || fresh {
		t.Fatal("partial reset must not become eligible")
	}
	cfg := filepath.Join(path, "monloader.toml")
	created, err := seedBundledMeloaderNew(cfg, "127.0.0.1:8455", "default")
	if err != nil || !created {
		t.Fatal("new config was not seeded")
	}
	created, err = seedBundledMeloaderNew(cfg, "127.0.0.1:8455", "default")
	if err != nil || created {
		t.Fatal("existing config must not be marked newly seeded")
	}
}
