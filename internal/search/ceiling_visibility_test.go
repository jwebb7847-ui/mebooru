package search

import "testing"

func TestRatedFilterKeepsCeilingShape(t *testing.T) {
	ceiling := ApplyCeiling(nil, "general")
	combined := AndExpr{Left: ceiling, Right: FilterExpr{Key: "rated", Val: "true"}}
	user, rank, ok := extractCeilingShape(combined)
	if !ok || rank != 0 || !isInternalRatedFilter(user) {
		t.Fatalf("ceiling parsing lost rated-only filter: ok=%v rank=%d user=%#v", ok, rank, user)
	}
	if !fastTotalExact(combined) {
		t.Fatal("rank-bound rated-only count is exact")
	}
}
