package web

import (
	"path/filepath"
	"testing"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/shared/apitoken"
	"github.com/monbooru/monbooru/internal/shared/bundlepair"
	"github.com/monbooru/monbooru/internal/shared/contract"
)

func TestBundledPairPersistenceAndScope(t *testing.T) {
	c := config.Default()
	path := filepath.Join(t.TempDir(), "monbooru.toml")
	s := &Server{cfg: c, configPath: path}
	secrets := bundlepair.Secrets{Library: apitoken.NewSecret(), Loader: apitoken.NewSecret()}
	if err := s.PersistBundledPair(secrets); err != nil {
		t.Fatal(err)
	}
	check := func(c *config.Config) {
		t.Helper()
		tok := c.Auth.Tokens.Paired(contract.AppMonloader)
		if tok == nil || tok.TokenHash != apitoken.Hash(secrets.Library) ||
			tok.PeerURL != "http://127.0.0.1:"+contract.PortMonloader ||
			!tok.HasScope(config.ScopeRead) || !tok.HasScope(config.ScopeWrite) ||
			tok.HasScope(config.ScopeDelete) || c.Monloader.APIToken != secrets.Loader {
			t.Fatal("unexpected paired credentials or scopes")
		}
	}
	check(c)
	loaded, err := config.LoadWithDefaults(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	check(loaded)
	if err := s.PersistBundledPair(bundlepair.Secrets{Library: apitoken.NewSecret(), Loader: apitoken.NewSecret()}); err == nil {
		t.Fatal("automatic credential replacement was accepted")
	}
	if err := s.RollbackBundledPair(bundlepair.Secrets{}); err == nil {
		t.Fatal("rollback removed someone else's pairing")
	}
	check(c)
	if err := s.RollbackBundledPair(secrets); err != nil {
		t.Fatal(err)
	}
	if c.Auth.Tokens.Paired(contract.AppMonloader) != nil || c.Monloader.APIToken != "" {
		t.Fatal("rollback did not remove exact bootstrap grant")
	}
}
