package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/shared/apitoken"
	"github.com/monbooru/monloader/internal/shared/bundlepair"
	"github.com/monbooru/monloader/internal/shared/contract"
)

const bundledLibraryURL = "http://127.0.0.1:" + contract.PortMonbooru
const bundledLoaderBind = "127.0.0.1:" + contract.PortMonloader

// This is deliberately narrower than ordinary manual pairing. Both the
// effective and file-layer configurations must still be the pristine values
// seeded by the bundled Mebooru launcher.
func pristineBundledConfig(c *config.Config) bool {
	return c.SetupDone &&
		c.Server.BindAddress == bundledLoaderBind &&
		c.Server.BaseURL == "http://"+bundledLoaderBind &&
		c.Monbooru.APIURL == bundledLibraryURL &&
		c.Monbooru.WebURL == bundledLibraryURL &&
		c.Monbooru.APIToken == "" && !c.Monbooru.Paused &&
		len(c.Auth.Tokens) == 0 && !c.Auth.EnablePassword &&
		!c.PTR.Enabled
}

func persistBundledPair(p *config.Provider, s bundlepair.Secrets) error {
	tok := apitoken.FromSecret(contract.AppMonbooru+" (paired)", s.Loader, config.AllScopes)
	tok.Paired = contract.AppMonbooru
	return p.Update(func(c *config.Config) error {
		if !pristineBundledConfig(c) {
			return errors.New("bundle bootstrap requires pristine local Meloader configuration")
		}
		c.Auth.Tokens = append(c.Auth.Tokens, tok)
		c.Monbooru.APIToken = s.Library
		return nil
	})
}

func rollbackBundledPair(p *config.Provider, s bundlepair.Secrets) error {
	return p.Update(func(c *config.Config) error {
		tok := c.Auth.Tokens.Paired(contract.AppMonbooru)
		if tok == nil || tok.TokenHash != apitoken.Hash(s.Loader) ||
			c.Monbooru.APIToken != s.Library {
			return errors.New("bundle bootstrap rollback refused a changed pairing")
		}
		c.Auth.Tokens.Remove(tok.ID)
		c.Monbooru.APIToken = ""
		return nil
	})
}

// Only the macOS bundle invokes this internal startup flag, passing two
// anonymous pipes as descriptors 3 (from parent) and 4 (to parent).
// No secret is present in a URL, argument, environment variable or log.
// The launcher closes its ends on timeout or failure, so this handshake
// cannot keep the child waiting when its actual parent exits.
func bootstrapFromParent(p *config.Provider, path string, desktopMode, noBrowser bool) error {
	if runtime.GOOS != "darwin" || !desktopMode || !noBrowser {
		return errors.New("bundle bootstrap is only available to the desktop companion")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	if filepath.Clean(path) != filepath.Join(dir, "monloader", "monloader.toml") ||
		!pristineBundledConfig(p.Current()) {
		return errors.New("bundle bootstrap refused nonstandard or previously configured profile")
	}
	in := os.NewFile(3, "bundle-bootstrap-read")
	out := os.NewFile(4, "bundle-bootstrap-write")
	if in == nil || out == nil {
		return errors.New("bundle bootstrap missing inherited pipes")
	}
	defer in.Close()
	defer out.Close()
	for _, f := range []*os.File{in, out} {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return fmt.Errorf("bundle bootstrap requires inherited anonymous pipes")
		}
	}
	return bundlepair.Child(in, out,
		func(s bundlepair.Secrets) error { return persistBundledPair(p, s) },
		func(s bundlepair.Secrets) error { return rollbackBundledPair(p, s) },
	)
}
