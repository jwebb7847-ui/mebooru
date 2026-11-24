package relations

import (
	"strings"
	"testing"
)

func TestUnratedPairVisibility(t *testing.T) {
	rank := 0
	normal, _ := ceilingClause(&rank)
	strict, args := ceilingClause(&rank, true)
	if strings.Contains(normal, "rating_rank >= 0") || strings.Count(strict, "rating_rank >= 0") != 2 {
		t.Fatalf("unrated constraints: normal=%q strict=%q", normal, strict)
	}
	if len(args) != 1 || args[0] != rank {
		t.Fatalf("incorrect args %#v", args)
	}
	if clause, _ := ceilingClause(nil, true); clause != "" {
		t.Fatalf("unrestricted mode should not hide unrated: %q", clause)
	}
}
