package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jwebb7847-ui/mebooru/shared/fsx"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

func migrateLegacyAcquisition(ctx context.Context, libraryConfig, legacyConfig, state, gallery string) (string, error) {
	base, err := filepath.Abs(filepath.Dir(libraryConfig))
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, "managed-acquisition")
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("This library already has a managed acquisition folder. Keep it intact and use a separate profile to try migration again.")
	}
	staged, err := os.MkdirTemp(base, ".migration-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	for _, sub := range []string{"backup", "queue", "stage", "windows"} {
		if err := os.Mkdir(filepath.Join(staged, sub), 0700); err != nil {
			return "", err
		}
	}
	if err := copyMigrationFile(libraryConfig, filepath.Join(staged, "backup", "monbooru.toml")); err != nil {
		return "", fmt.Errorf("backing up the library configuration: %w", err)
	}
	prepared := filepath.Join(staged, bundledPolicyConfig)
	if legacyConfig == "" {
		if state != "" {
			return "", errors.New("Choose the Meloader configuration for this state folder.")
		}
		err = seedBundledPolicy(prepared)
	} else {
		err = importLegacyLoader(ctx, legacyConfig, state, gallery, staged, target)
	}
	if err != nil {
		return "", err
	}
	if _, err := readLiveConfig(prepared); err != nil {
		return "", fmt.Errorf("the copied acquisition settings cannot be used: %w", err)
	}
	policy, err := acquisitionpolicy.Open(staged)
	if err != nil {
		return "", err
	}
	if err := errors.Join(policy.Save(true), policy.Close()); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(staged, target); err != nil {
		return "", err
	}
	return filepath.Join(target, bundledPolicyConfig), nil
}

func importLegacyLoader(ctx context.Context, config, state, gallery, stage, target string) error {
	if !filepath.IsAbs(config) || state != "" && !filepath.IsAbs(state) {
		return errors.New("Enter absolute paths to the old configuration and state folder.")
	}
	source := filepath.Dir(config)
	if state == "" {
		state = source
		for _, name := range []string{"queue.sqlite", "gallery-dl-archive.sqlite", "ptr/ptr.sqlite"} {
			if _, err := os.Stat(filepath.Join(source, "data", filepath.FromSlash(name))); err == nil {
				state = filepath.Join(source, "data")
				break
			}
		}
	}
	if info, err := os.Stat(state); err != nil || !info.IsDir() {
		return errors.New("The Meloader state folder could not be read.")
	}
	backup := filepath.Join(stage, "backup", "monloader.toml")
	if err := copyMigrationFile(config, backup); err != nil {
		return fmt.Errorf("reading the old Meloader configuration: %w", err)
	}
	var doc map[string]any
	if _, err := toml.DecodeFile(backup, &doc); err != nil {
		return errors.New("The old Meloader configuration could not be read as TOML.")
	}
	section := func(name string) map[string]any {
		table, _ := doc[name].(map[string]any)
		if table == nil {
			table = make(map[string]any)
			doc[name] = table
		}
		return table
	}
	path := func(table map[string]any, key, fallback string) string {
		val, _ := table[key].(string)
		if val == "" {
			return fallback
		}
		if !filepath.IsAbs(val) {
			return filepath.Join(source, val)
		}
		return val
	}
	gdl, ptr, download := section("gallerydl"), section("ptr"), section("downloader")
	if binary, _ := gdl["binary_path"].(string); binary != "" && binary != "gallery-dl" {
		gdl["binary_path"] = path(gdl, "binary_path", "")
	}
	archive := path(gdl, "archive_path", filepath.Join(state, "gallery-dl-archive.sqlite"))
	if err := snapshotLegacyDB(ctx, archive, filepath.Join(stage, "gallery-dl-archive.sqlite"), false); err != nil {
		return fmt.Errorf("copying the old download archive: %w", err)
	}
	gdl["archive_path"] = filepath.Join(target, "gallery-dl-archive.sqlite")
	gdl["config_path"] = filepath.Join(target, "gallery-dl.json")
	cookies := path(gdl, "cookies_dir", filepath.Join(state, "cookies"))
	if err := copyMigrationTree(ctx, cookies, filepath.Join(stage, "cookies")); err != nil {
		return fmt.Errorf("copying cookie files: %w", err)
	}
	gdl["cookies_dir"] = filepath.Join(target, "cookies")
	if sites, _ := doc["sites"].([]map[string]any); len(sites) != 0 {
		for i, site := range sites {
			if p := path(site, "cookies", ""); p != "" {
				name := fmt.Sprintf("source-cookies-%d", i+1)
				if err := copyMigrationFile(p, filepath.Join(stage, name)); err != nil {
					return fmt.Errorf("copying a source cookie file: %w", err)
				}
				site["cookies"] = filepath.Join(target, name)
			}
		}
	}
	if err := copyMigrationTree(ctx, filepath.Join(source, "profiles"), filepath.Join(stage, "profiles")); err != nil {
		return fmt.Errorf("copying source profiles: %w", err)
	}
	ptrDir := path(ptr, "data_path", filepath.Join(state, "ptr"))
	if err := os.Mkdir(filepath.Join(stage, "ptr"), 0700); err != nil {
		return err
	}
	for _, name := range []string{"ptr.sqlite", "contrib.sqlite"} {
		if err := snapshotLegacyDB(ctx, filepath.Join(ptrDir, name), filepath.Join(stage, "ptr", name), false); err != nil {
			return fmt.Errorf("copying the local PTR index: %w", err)
		}
	}
	ptr["data_path"] = filepath.Join(target, "ptr")
	if ca := path(ptr, "tls_ca_file", ""); ca != "" {
		if err := copyMigrationFile(ca, filepath.Join(stage, "ptr-ca.pem")); err != nil {
			return fmt.Errorf("copying the PTR certificate: %w", err)
		}
		ptr["tls_ca_file"] = filepath.Join(target, "ptr-ca.pem")
	}
	defaultGallery, _ := section("monbooru")["default_gallery"].(string)
	if defaultGallery == "" {
		defaultGallery = gallery
	}
	delete(doc, "auth")
	delete(doc, "monbooru")
	section("tags")["strict_blacklist"] = true
	limit, _ := download["max_items_per_job"].(int64)
	if limit < 1 {
		limit = 200
	}
	limit = min(limit, workerproto.MaxMetadataWindow)
	download["max_items_per_job"] = limit
	dst := filepath.Join(stage, "queue", "queue.sqlite")
	if err := snapshotLegacyDB(ctx, filepath.Join(state, "queue.sqlite"), dst, false); err != nil {
		return fmt.Errorf("copying acquisition history: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		if err := parkLegacyQueue(ctx, dst, defaultGallery, limit); err != nil {
			return fmt.Errorf("preparing copied jobs and watches: %w", err)
		}
	}
	return fsx.WriteDurable(filepath.Join(stage, bundledPolicyConfig), func(f *os.File) error {
		return toml.NewEncoder(f).Encode(doc)
	})
}

func sqliteLegacyPath(path string) string {
	return "file:" + strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(filepath.ToSlash(path))
}

func snapshotLegacyDB(ctx context.Context, src, dst string, required bool) error {
	info, err := os.Stat(src)
	if !required && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("expected a readable SQLite file")
	}
	db, err := sql.Open("sqlite", sqliteLegacyPath(src)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", filepath.ToSlash(dst)); err != nil {
		return err
	}
	return os.Chmod(dst, 0600)
}

func parkLegacyQueue(ctx context.Context, path, gallery string, limit int64) error {
	db, err := sql.Open("sqlite", sqliteLegacyPath(path))
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET status='interrupted',
		data=json_set(data,'$.job.status','interrupted','$.job.finished_at',?,'$.status_changed_at',?,
		'$.job.error','Imported unfinished job. Retry when ready.',
		'$.job.gallery',COALESCE(NULLIF(json_extract(data,'$.job.gallery'),''),?),
		'$.job.max_items',MIN(?,MAX(1,COALESCE(NULLIF(json_extract(data,'$.job.max_items'),0),?))))
		WHERE status IN ('queued','running')`, now, now, gallery, limit, limit)
	if err != nil {
		return err
	}
	// Existing managed startup also accepts a legacy watch table without
	// destination or cap. Supply the old default before opening the queue.
	for _, col := range []struct{ name, def string }{
		{"gallery", "TEXT NOT NULL DEFAULT ''"}, {"max_items", "INTEGER NOT NULL DEFAULT 0"},
	} {
		var present int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_xinfo('watches') WHERE name=?", col.name).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE watches ADD COLUMN "+col.name+" "+col.def); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE watches SET gallery=COALESCE(NULLIF(gallery,''),?),
		interval_hours=CASE WHEN max_items<0 THEN 0 ELSE interval_hours END,
		max_items=MIN(?,CASE WHEN max_items<=0 THEN ? ELSE max_items END)`, gallery, limit, limit); err != nil {
		return err
	}
	return tx.Commit()
}

func copyMigrationFile(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return errors.New("expected a regular profile file")
	}
	return fsx.WriteDurable(dst, func(out *os.File) error {
		_, err := io.Copy(out, f)
		return err
	})
}

func copyMigrationTree(ctx context.Context, src, dst string) error {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		where, _ := filepath.Rel(src, path)
		out := filepath.Join(dst, where)
		if entry.IsDir() {
			return os.Mkdir(out, 0700)
		}
		if entry.Type() != 0 {
			return errors.New("profile directories must contain regular files")
		}
		return copyMigrationFile(path, out)
	})
}
