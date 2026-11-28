package library

import (
	"cmp"
	"fmt"

	"github.com/monbooru/monbooru/internal/gallery"
)

// ApplyInitialTags preserves the legacy add-only arrival path. Client
// tags are attributed to via, not to the source's refetch ownership.
// It is not by itself a managed authorization or completion witness.
func (g *Gallery) ApplyInitialTags(imgID int64, rawTags []string, via string) (added int, warnings, rejected []string, err error) {
	// Default "api" so API tags don't read as anonymous UI adds.
	via = cmp.Or(via, "api")
	tagIDs, warnings, rejected, err := gallery.ResolveTagNames(g.DB, g.TagSvc, rawTags, via)
	if err != nil || len(tagIDs) == 0 {
		return 0, warnings, rejected, err
	}
	results, err := g.TagSvc.AddTagsToOneImage(imgID, tagIDs, via)
	if err != nil {
		return 0, warnings, rejected, fmt.Errorf("apply tags: %w", err)
	}
	for _, r := range results {
		if r.Added {
			added++
		}
	}
	return added, warnings, rejected, nil
}
