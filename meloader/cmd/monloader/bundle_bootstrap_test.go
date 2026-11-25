package main

import (
	"path/filepath"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/shared/apitoken"
	"github.com/monbooru/monloader/internal/shared/bundlepair"
)

func freshBundleConfig() *config.Config {
	c := config.Default()
	c.SetupDone = true
	c.Server.BindAddress = bundledLoaderBind
	c.Server.BaseURL = "http://" + bundledLoaderBind
	c.Monbooru.APIURL = bundledLibraryURL
	c.Monbooru.WebURL = bundledLibraryURL
	c.PTR.Enabled = false
	return c
}

func TestBundledPairPersistAndRollback(t *testing.T) {
	c := freshBundleConfig()
	path := filepath.Join(t.TempDir(), "monloader.toml")
	if err := config.Save(c, path); err != nil {
		t.Fatal(err)
	}
	p := config.NewProvider(c)
	p.Persist(path, "")
	secrets := bundlepair.Secrets{Library: apitoken.NewSecret(), Loader: apitoken.NewSecret()}
	if err := persistBundledPair(p, secrets); err != nil {
		t.Fatal(err)
	}
	check := func(c *config.Config) {
		t.Helper()
		tok := c.Auth.Tokens.Paired(contract.AppMonbooru)
		if tok == nil || tok.TokenHash != apitoken.Hash(secrets.Loader) ||
			c.Monbooru.APIToken != secrets.Library || c.Monbooru.Paused {
			t.Fatal("incorrect bootstrap credentials")
		}
		for _, scope := range config.AllScopes {
			if !tok.HasScope(scope) {
				t.Fatal("missing existing Meloader API scope")
			}
		}
	}
	check(p.Current())
	saved, err := config.LoadFromFileIn(path, "")
	if err != nil {
		t.Fatal(err)
	}
	check(saved)
	if err := persistBundledPair(p, secrets); err == nil {
		t.Fatal("existing credentials must not be replaced")
	}
	if err := rollbackBundledPair(p, bundlepair.Secrets{}); err == nil {
		t.Fatal("rollback accepted incorrect secrets")
	}
	if err := rollbackBundledPair(p, secrets); err != nil {
		t.Fatal(err)
	}
	if p.Current().Auth.Tokens.Paired(contract.AppMonbooru) != nil ||
		p.Current().Monbooru.APIToken != "" {
		t.Fatal("rollback left credentials behind")
	}
}

func TestBundledPairRejectsNonlocalOrExistingConfiguration(t *testing.T) {
	cases := map[string]func(*config.Config){
		"remote library": func(c *config.Config) { c.Monbooru.APIURL = "https://example.invalid" },
		"remote web":     func(c *config.Config) { c.Monbooru.WebURL = "https://example.invalid" },
		"external bind":  func(c *config.Config) { c.Server.BindAddress = "0.0.0.0:8456" },
		"paused":         func(c *config.Config) { c.Monbooru.Paused = true },
		"credential":     func(c *config.Config) { c.Monbooru.APIToken = "existing" },
		"PTR enabled":    func(c *config.Config) { c.PTR.Enabled = true },
		"manual token":   func(c *config.Config) { c.Auth.Tokens = append(c.Auth.Tokens, apitoken.Token{}) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := freshBundleConfig()
			mutate(c)
			if pristineBundledConfig(c) {
				t.Fatal("nonpristine config accepted")
			}
		})
	}
}
