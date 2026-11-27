package library

import(
	"github.com/jwebb7847-ui/mebooru/shared/logx"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/relations"
)

// LinkParentRelations is the existing best-effort import policy.
// It preserves prior operator-declared derivative sources and links
// both parent-first and child-first arrivals. The HTTP path keeps
// its existing behavior. A managed caller must independently observe
// actual relationship outcomes before claiming a terminal receipt.
func (g *Gallery) LinkParentRelations(imageID int64, url, parentURL string) {
	if g.RelationsSvc == nil {
		return
	}
	if parentURL != "" {
		if parentID, ok := gallery.ImageIDBySourceURL(g.DB, parentURL); ok && parentID != imageID {
			linkFirstSource(g, parentID, imageID)
		}
	}
	if url == "" {
		return
	}
	children, err := gallery.ChildIDsByParentURL(g.DB, url)
	if err != nil {
		logx.Warnf("api: child lookup for %q: %v", url, err)
		return
	}
	for _, child := range children {
		if child == imageID {
			continue
		}
		linkFirstSource(g, imageID, child)
	}
}

// Only while the derivative names no source: a fetch must not stack its
// claim on one the operator declared by hand.
func linkFirstSource(g *Gallery, source, derivative int64) {
	has, err := relations.HasDerivativeSource(g.DB, derivative)
	if err != nil {
		logx.Debugf("api: parent link %d -> %d skipped: %v", source, derivative, err)
		return
	}
	if has {
		return
	}
	if err := g.RelationsSvc.AddDerivativeEdge(source, derivative); err != nil {
		logx.Debugf("api: parent link %d -> %d skipped: %v", source, derivative, err)
	}
}

