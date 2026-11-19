package counts

import (
	"context"
	"fmt"

	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/models"
)

const catalogTagRows = ` FROM tags WHERE is_alias = 0 AND rejected = 0`

type GalleryStats struct {
	Images     int64
	Videos     int64
	Archives   int64
	MediaBytes int64
	Tags       int64
	TagsInUse  int64
}

// ReadGalleryStats counts the whole gallery from indexed files and tag usage.
func ReadGalleryStats(ctx context.Context, database *db.DB) (GalleryStats, error) {
	type mediaRow struct {
		fileType string
		count    int64
		bytes    int64
	}
	media, err := db.QueryAllContext(ctx, database.Read, db.ScanRow(func(row *mediaRow) []any {
		return []any{&row.fileType, &row.count, &row.bytes}
	}), `SELECT file_type, COUNT(*), SUM(file_size)
		FROM images WHERE is_missing = 0 GROUP BY file_type`)
	if err != nil {
		return GalleryStats{}, fmt.Errorf("read gallery media counts: %w", err)
	}

	var stats GalleryStats
	for _, row := range media {
		stats.MediaBytes += row.bytes
		format, known := models.LookupFileType(row.fileType)
		switch {
		case format.Video:
			stats.Videos += row.count
		case format.Kind == "archive":
			stats.Archives += row.count
		case known:
			stats.Images += row.count
		}
	}

	// usage_count covers present media. Each matching catalog record counts once.
	err = database.Read.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE usage_count > 0)`+catalogTagRows,
	).Scan(&stats.Tags, &stats.TagsInUse)
	if err != nil {
		return GalleryStats{}, fmt.Errorf("read gallery tag counts: %w", err)
	}
	return stats, nil
}
