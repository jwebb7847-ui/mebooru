package library

import (
	"strings"
	"testing"

	"github.com/monbooru/monbooru/internal/search"
)

func TestUnratedVisibilityFollowsRestrictedCeiling(t *testing.T) {
	restricted := NewCeiling("general", nil, true)
	if !restricted.HideUnrated() || restricted.CacheKey() != "general|hide-unrated" {
		t.Fatal("restricted ceiling should use a distinct hidden-unrated view")
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
		t.Fatalf("single-image relation filter omitted unrated constraint: %q", one)
	}
	if two, _ := restricted.WhereTwo("a_image_id", "b_image_id"); strings.Count(two, "rated.rating_rank >= 0") != 2 {
		t.Fatalf("pair relation filter omitted unrated constraint: %q", two)
	}
	if group, _ := restricted.WhereGroupClean("dup_group_members", "dup_groups.id"); !strings.Contains(group, "i.rating_rank < 0") {
		t.Fatalf("group relation filter omitted unrated constraint: %q", group)
	}

	allowed := NewCeiling("general", nil, false)
	if allowed.HideUnrated() || allowed.CacheKey() != "general" {
		t.Fatal("explicitly included unrated files should use the existing ceiling")
	}
	unrestricted := NewCeiling("explicit", nil, true)
	if unrestricted.HideUnrated() || unrestricted.Apply(nil) != nil || unrestricted.RankFilter().Active {
		t.Fatal("explicit ceiling must show everything, including unrated")
	}
}
