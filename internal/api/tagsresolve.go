package api

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/models"
	"github.com/monbooru/monbooru/internal/tags"
)

const maxResolveNames = workerproto.MaxResolveNames

type resolvedTag = workerproto.TagResolution

// resolveTags answers what the gallery would do with each name on arrival,
// without creating anything.
func (h *Handler) resolveTags(w http.ResponseWriter, r *http.Request) {
	g, ok := h.resolveGallery(w, r)
	if !ok {
		return
	}
	var body struct {
		Names []string `json:"names"`
	}
	if !contract.DecodeJSON(w, r, &body, maxJSONBody) {
		return
	}
	if len(body.Names) == 0 || len(body.Names) > maxResolveNames {
		contract.WriteError(w, http.StatusBadRequest, "invalid_request", "names takes 1 to 500 entries")
		return
	}
	results, err := resolveTagNames(context.Background(), g, body.Names, 0)
	if serverError(w, err) {
		return
	}
	contract.WriteJSON(w, http.StatusOK, map[string]any{"results": results})
}

// Shared read-only resolution. The legacy caller retains its existing
// limits; managed requests additionally bound aggregate implication output.
// Context is checked between DB operations (not a cancellable DB query).
func resolveTagNames(ctx context.Context, g *library.Gallery, names []string, maxImplications int) ([]resolvedTag, error) {
	results := make([]resolvedTag, 0, len(names))
	implied := make([][]int64, len(names))
	var impliedIDs []int64
	for i, raw := range names {
		if err := ctx.Err(); err != nil { return nil, err }
		res := resolvedTag{Name: raw, Implies: []string{}}
		catID, bare, err := gallery.ResolveCategoryTag(g.DB, raw)
		if err != nil && !errors.Is(err, tags.ErrEmptyFormName) {
			return nil, err
		}
		name, verr := bare, err
		if verr == nil {
			name, verr = g.TagSvc.ValidateTagIn(bare, catID)
		}
		if verr != nil {
			res.Status, res.Reason = "refused", verr.Error()
			results = append(results, res)
			continue
		}
		tag, err := g.TagSvc.FindTag(name, catID)
		switch {
		case errors.Is(err, tags.ErrTagNotFound):
			res.Status = "new"
		case err != nil:
			return nil, err
		case tag.Rejected:
			res.Status = "rejected"
		case tag.Name != name || tag.CategoryID != catID:
			res.Status, res.ResolvesTo = "alias", qualifiedName(tag.CategoryName, tag.Name)
		default:
			res.Status = "exists"
		}
		if err == nil && !tag.Rejected {
			closure, err := g.TagSvc.ImplicationClosure(tag.ID)
			if err != nil { return nil, err }
			if len(closure) == 0 { return nil, errors.New("empty implication closure") }
			if maxImplications > 0 && len(closure)-1 > maxImplications-len(impliedIDs) {
				return nil, workerproto.ErrMessage
			}
			implied[i] = closure[1:]
			impliedIDs = append(impliedIDs, closure[1:]...)
		}
		results = append(results, res)
	}
	if err := ctx.Err(); err != nil { return nil, err }
	byID, err := g.TagSvc.GetTags(impliedIDs)
	if err != nil { return nil, err }
	for i, ids := range implied {
		names := make([]*models.Tag, 0, len(ids))
		for _, id := range ids {
			if t := byID[id]; t != nil {
				names = append(names, t)
			} else if maxImplications > 0 {
				// A concurrent change must not yield incomplete managed evidence.
				return nil, workerproto.ErrMessage
			}
		}
		slices.SortFunc(names, func(a, b *models.Tag) int { return cmp.Compare(a.Name, b.Name) })
		for _, t := range names {
			results[i].Implies = append(results[i].Implies, qualifiedName(t.CategoryName, t.Name))
		}
	}
	if err := ctx.Err(); err != nil { return nil, err }
	return results, nil
}

func qualifiedName(category, name string) string {
	if category == "general" || strings.TrimSpace(category) == "" {
		return name
	}
	return category + ":" + name
}
