package ptr

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monbooru/monloader/internal/config"
)

func TestRepositoryRequiresTrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ptr/metadata" || r.URL.Query().Get("since") != "12" || r.Header.Get("Hydrus-Key") != "fixture-key" {
			t.Errorf("unexpected repository request: %s", r.URL)
		}
		_, _ = w.Write([]byte("fixture"))
	}))
	defer server.Close()

	cfg := config.PTRConfig{Address: server.URL + "/ptr/", AccessKey: "fixture-key"}
	query := url.Values{"since": {"12"}}
	read := func() ([]byte, error) {
		return newClient(cfg).get(context.Background(), "/metadata", query)
	}
	if _, err := read(); err == nil || IsRetryable(err) || !strings.Contains(err.Error(), "verifying the PTR certificate") {
		t.Fatalf("untrusted certificate: %v", err)
	}
	cfg.TLSCAFile = filepath.Join(t.TempDir(), "repository.pem")
	if err := os.WriteFile(cfg.TLSCAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := read(); err != nil || string(data) != "fixture" {
		t.Fatalf("trusted repository: %s, %v", data, err)
	}
	if data, err := newClient(cfg).doContrib(context.Background(), http.MethodGet, "/metadata", query, nil); err != nil || string(data) != "fixture" {
		t.Fatalf("trusted contribution request: %s, %v", data, err)
	}
	cfg.TLSCAFile += ".missing"
	if _, err := read(); err == nil || !strings.Contains(err.Error(), "ptr.tls_ca_file") {
		t.Fatalf("missing trust file: %v", err)
	}
}

func TestPTRDeleteKeepsUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{dbFile, contribFile, dbFile + "-wal", "other.sqlite"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	engine := NewEngine(config.PTRConfig{DataPath: dir})
	if err := engine.Delete(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "other.sqlite" {
		t.Fatalf("remaining files: %v, %v", files, err)
	}
}
