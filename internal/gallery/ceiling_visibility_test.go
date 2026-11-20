package gallery

import (
    "strings"
    "testing"
)

func TestRankCeilingUnratedVisibility(t *testing.T) {
    for _, tc := range []struct{
        ceiling RankCeiling
        contains string
    }{
        {RankCeiling{}, ""},
        {RankCeiling{Rank:0, Active:true}, "rating_rank <= ?"},
        {RankCeiling{Rank:0, Active:true, ExcludeUnrated:true}, "rating_rank BETWEEN 0 AND ?"},
    } {
        where, args := tc.ceiling.and("i")
        if !strings.Contains(where, tc.contains) {
            t.Fatalf("where %q missing %q", where, tc.contains)
        }
        if tc.ceiling.Active && (len(args)!=1 || args[0] != tc.ceiling.Rank) {
            t.Fatalf("unexpected arguments %#v", args)
        }
        if !tc.ceiling.Active && (where != "" || len(args)!=0) {
            t.Fatalf("inactive ceiling modified query %q %#v", where, args)
        }
    }
}
