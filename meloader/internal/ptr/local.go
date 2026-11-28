package ptr

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/config"
)

// ReadLocalTags uses the same display-tag query as the standalone PTR.
// This path never creates, upgrades or starts synchronization of an index.
func ReadLocalTags(ctx context.Context, cfg config.PTRConfig, hash string) (workerproto.PTRLocalTags, error) {
	v := workerproto.PTRLocalTags{Enabled: cfg.Enabled}
	if !cfg.Enabled || !IndexExists(cfg.DataPath) {
		return v, nil
	}
	path := filepath.Join(cfg.DataPath, dbFile)
	db, err := sql.Open("sqlite", "file:"+uriPathEscaper.Replace(filepath.ToSlash(path))+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)")
	if err != nil {
		return v, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return v, err
	}
	version, ok, err := syncValue(db, "schema")
	if err != nil {
		return v, err
	}
	if !ok || version != strconv.Itoa(schemaVersion) {
		return v, errSchemaMismatch
	}
	v.Indexed = true
	if hash == "" {
		return v, nil
	}
	v.Tags, v.Found, err = (&Store{db: db}).TagsForHash(hash)
	slices.Sort(v.Tags)
	return v, err
}
