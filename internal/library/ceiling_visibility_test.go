package library

import (
	"strings"
	"testing"

	"github.com/monbooru/monbooru/internal/search"
)

func TestUnratedVisibilityIndependentOfCeiling(t *testing.T) {
	restricted := NewCeiling("general", nil, true)
	if !restricted.HideUnrated() || restricted.CacheKey() != "general|hide-unrated" {
		t.Fatal("restricted ceiling must have its own rated-only cache")
	}
	e, ok := restricted.Apply(nil).(search.AndExpr)
	if !ok {
		t.Fatalf("restricted search must combine ceiling and rated-only filter: %T", restricted.Apply(nil))
	}
	if f, ok := e.Right.(search.FilterExpr); !ok || f.Key != "rated" || f.Val != "true" {
		t.Fatalf("unexpected rated-only filter: %#v", e.Right)
	}
	if got := restricted.RankFilter(); !got.Active || !got.ExcludeUnrated || got.Rank != 0 {
		t.Fatalf("rank visibility = %#v", got)
	}
	if one, _ := restricted.WhereOne("i.id"); !strings.Contains(one, "rated.rating_rank >= 0") {
		t.Fatalf("single-image filter omitted unrated: %q", one)
	}
	if two, _ := restricted.WhereTwo("a_image_id", "b_image_id"); strings.Count(two, "rated.rating_rank >= 0") != 2 {
		t.Fatalf("pair filter omitted unrated: %q", two)
	}
	if group, _ := restricted.WhereGroupClean("dup_group_members", "dup_groups.id"); !strings.Contains(group, "i.rating_rank < 0") {
		t.Fatalf("group filter omitted unrated: %q", group)
	}

	allowed := NewCeiling("general", nil, false)
	if allowed.HideUnrated() || allowed.CacheKey() != "general" {
		t.Fatal("lower ceiling must be able to include unrated")
	}
	full := NewCeiling("", nil, false)
	if full.HideUnrated() || full.VisibilityActive() || full.Apply(nil) != nil || full.RankFilter().Active {
		t.Fatal("unrestricted view must show all files")
	}
	topHidden := NewCeiling("", nil, true)
	if !topHidden.HideUnrated() || topHidden.IsActive() || !topHidden.VisibilityActive() {
		t.Fatal("top rating level must permit an independent unrated filter")
	}
	if f, ok := topHidden.Apply(nil).(search.FilterExpr); !ok || f.Key != "rated" {
		t.Fatalf("top rated-only predicate = %#v", topHidden.Apply(nil))
	}
	if got := topHidden.RankFilter(); !got.Active || !got.ExcludeUnrated || got.Rank != 3 {
		t.Fatalf("top hidden-unrated rank = %#v", got)
	}
	if topHidden.CacheKey() == full.CacheKey() {
		t.Fatal("rating-only and all views must have separate cache keys")
	}
	if two, _ := topHidden.WhereTwo("a_image_id", "b_image_id"); strings.Count(two, "rated.rating_rank >= 0") != 2 {
		t.Fatalf("top pair filter omitted unrated: %q", two)
	}
}
