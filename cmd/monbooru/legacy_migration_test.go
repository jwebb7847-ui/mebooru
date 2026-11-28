package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestLegacyMigrationCopiesHistoryAndKeepsOriginals(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	lib, loader := filepath.Join(root, "library"), filepath.Join(root, "loader")
	for _, dir := range []string{lib, loader, filepath.Join(loader, "ptr"), filepath.Join(loader, "profiles")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	execSQL := func(path, statement string) {
		t.Helper()
		db, err := sql.Open("sqlite", sqliteLegacyPath(path))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	originalConfig := filepath.Join(lib, "monbooru.toml")
	write(originalConfig, "setup_done = true\n")
	write(filepath.Join(lib, "original.jpg"), "existing gallery bytes")
	runtime, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(loader, "monloader.toml")
	write(legacy, "[gallerydl]\nbinary_path = '"+runtime+"'\n[tags]\nblacklist = ['violence']\n[ptr]\nenabled = true\ndata_path = 'ptr'\n[[sites]]\nname = 'derpibooru'\napi_key = 'fixture-key'\ncookies = 'fixture-cookie.txt'\noptions = '{\"filter\":42}'\n")
	write(filepath.Join(loader, "fixture-cookie.txt"), "fixture cookies")
	write(filepath.Join(loader, "profiles", "custom.toml"), "fixture profile")
	execSQL(filepath.Join(loader, "ptr", "ptr.sqlite"), "CREATE TABLE retained(value); INSERT INTO retained VALUES ('ptr');")
	execSQL(filepath.Join(loader, "gallery-dl-archive.sqlite"), "CREATE TABLE archive(entry); INSERT INTO archive VALUES ('derpibooru1');")
	execSQL(filepath.Join(loader, "queue.sqlite"), `CREATE TABLE jobs(id INTEGER,status TEXT,data TEXT);
		INSERT INTO jobs VALUES (7,'queued','{"job":{"id":7,"status":"queued","url":"https://derpibooru.org/images/1"}}');
		CREATE TABLE watches(id INTEGER,url TEXT,interval_hours INTEGER,max_items INTEGER,last TEXT);
		INSERT INTO watches VALUES (4,'https://derpibooru.org/images/1',6,0,'');`)
	policy, err := migrateLegacyAcquisition(ctx, originalConfig, legacy, "", "Imports")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(policy)
	if read(originalConfig) != read(filepath.Join(dir, "backup", "monbooru.toml")) ||
		read(legacy) != read(filepath.Join(dir, "backup", "monloader.toml")) ||
		read(filepath.Join(lib, "original.jpg")) != "existing gallery bytes" ||
		!strings.Contains(read(filepath.Join(dir, workerproto.AcquisitionPolicyFile)), "offline") {
		t.Fatal("originals or offline policy were lost")
	}
	live, err := readLiveConfig(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSourceOptions(dir, &live); err != nil {
		t.Fatal(err)
	}
	if live.SourceOptions["derpibooru"] == "" || live.SourceOptions["derpibooru"] == "-" ||
		!strings.Contains(read(sourceOptionsPath(dir, "derpibooru")), "fixture-key") ||
		read(filepath.Join(dir, "source-cookies-1")) != "fixture cookies" {
		t.Fatal("source settings or cookies were lost")
	}
	query := func(path, statement string) string {
		t.Helper()
		db, err := sql.Open("sqlite", sqliteLegacyPath(path)+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var result string
		if err := db.QueryRow(statement).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if query(filepath.Join(loader, "queue.sqlite"), "SELECT status FROM jobs") != "queued" ||
		query(filepath.Join(dir, "queue", "queue.sqlite"), "SELECT status FROM jobs") != "interrupted" ||
		query(filepath.Join(dir, "queue", "queue.sqlite"), "SELECT gallery FROM watches") != "Imports" ||
		query(filepath.Join(dir, "gallery-dl-archive.sqlite"), "SELECT entry FROM archive") != "derpibooru1" ||
		query(filepath.Join(dir, "ptr", "ptr.sqlite"), "SELECT value FROM retained") != "ptr" {
		t.Fatal("jobs, archive, watch destination or PTR data changed unexpectedly")
	}
	if _, err := migrateLegacyAcquisition(ctx, originalConfig, legacy, "", "Imports"); err == nil {
		t.Fatal("repeat migration overwrote existing managed data")
	}
}
