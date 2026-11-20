package library

import (
	"sync"
	"strings"

	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/search"
	"github.com/monbooru/monbooru/internal/shared/contract"
	"github.com/monbooru/monbooru/internal/shared/logx"
)

// Ceiling is shared by one request's goroutines; mu guards the lazy cache.
type Ceiling struct {
	level          string
	cx             *Gallery
	excludeUnrated bool

	mu             sync.Mutex
	excludedIDs    []int64
	excludedLoaded bool
}

// Level is the raw cookie value; "explicit" means no ceiling too, so gate
// on IsActive.
func (c *Ceiling) Level() string {
	if c == nil {
		return ""
	}
	return c.level
}

func (c *Ceiling) IsActive() bool {
	if c == nil {
		return false
	}
	return c.level != "" && c.level != "explicit"
}

func (c *Ceiling) HideUnrated() bool {
	return c != nil && c.IsActive() && c.excludeUnrated
}

// Distinguish the two visibility modes in cached search result keys.
func (c *Ceiling) CacheKey() string {
	if c.HideUnrated() {
		return c.Level() + "|hide-unrated"
	}
	return c.Level()
}

func (c *Ceiling) Apply(userExpr search.Expr) search.Expr {
	expr := search.ApplyCeiling(userExpr, c.Level())
	if !c.HideUnrated() {
		return expr
	}
	// Use the stored rank rather than an image_tags scan per row.
	rated := search.FilterExpr{Key: "rated", Val: "true"}
	if expr == nil {
		return rated
	}
	return search.AndExpr{Left: expr, Right: rated}
}

func (c *Ceiling) ExcludedTagIDs() []int64 {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.excludedLoaded {
		return c.excludedIDs
	}
	if !c.IsActive() {
		return nil
	}
	c.excludedLoaded = true
	if c.cx == nil || c.cx.TagSvc == nil {
		return nil
	}
	c.excludedIDs = c.cx.TagSvc.RatingTagIDsAbove(c.level)
	return c.excludedIDs
}

func notExistsRatingPredicate(col, in string) string {
	return `NOT EXISTS (SELECT 1 FROM image_tags it WHERE it.image_id = ` + col + ` AND it.tag_id IN (` + in + `))`
}

// Empty when inactive, so the caller can omit the WHERE and keep the
// covering scan.
func (c *Ceiling) WhereOne(col string) (string, []any) {
	ids := c.ExcludedTagIDs()
	var predicates []string
	var args []any
	if len(ids) > 0 {
		in, a := db.InPlaceholders(ids)
		predicates = append(predicates, notExistsRatingPredicate(col, in))
		args = append(args, a...)
	}
	if c.HideUnrated() {
		predicates = append(predicates, "EXISTS (SELECT 1 FROM images rated WHERE rated.id = "+col+" AND rated.rating_rank >= 0)")
	}
	return strings.Join(predicates, " AND "), args
}

func (c *Ceiling) WhereTwo(leftCol, rightCol string) (string, []any) {
	left, a := c.WhereOne(leftCol)
	right, b := c.WhereOne(rightCol)
	if left == "" || right == "" {
		return "", nil
	}
	return left + " AND " + right, append(a, b...)
}

func (c *Ceiling) WhereGroupClean(membersTable, groupCol string) (string, []any) {
	ids := c.ExcludedTagIDs()
	var parts []string
	var args []any
	if len(ids) > 0 {
		in, a := db.InPlaceholders(ids)
		parts = append(parts, `NOT EXISTS (
			SELECT 1 FROM `+membersTable+` mr
			JOIN image_tags it ON it.image_id = mr.image_id
			WHERE mr.group_id = `+groupCol+` AND it.tag_id IN (`+in+`)
		)`)
		args = append(args, a...)
	}
	if c.HideUnrated() {
		parts = append(parts, `NOT EXISTS (
			SELECT 1 FROM `+membersTable+` mr
			JOIN images i ON i.id = mr.image_id
			WHERE mr.group_id = `+groupCol+` AND i.rating_rank < 0
		)`)
	}
	return strings.Join(parts, " AND "), args
}

// Rows pass with rank_col <= rank, so the unrated -1 passes every level.
func (c *Ceiling) RankCeiling() (int, bool) {
	if c == nil || !c.IsActive() {
		return 0, false
	}
	return contract.RatingRank(c.level), true
}

func (c *Ceiling) RankFilter() gallery.RankCeiling {
	rank, active := c.RankCeiling()
	return gallery.RankCeiling{Rank: rank, Active: active, ExcludeUnrated: c.HideUnrated()}
}

// TaintedAmong reads, in one query, the over-ceiling ids among the
// endpoints of table's colA and colB. A read error keeps every row visible
// rather than failing the page.
func (c *Ceiling) TaintedAmong(table, colA, colB string) map[int64]bool {
	rank, active := c.RankCeiling()
	if !active || c.cx == nil || c.cx.DB == nil {
		return nil
	}
	tainted := map[int64]bool{}
	err := db.QueryIDsFunc(c.cx.DB.Read, func(id int64) bool {
		tainted[id] = true
		return true
	}, `SELECT id FROM images WHERE (rating_rank > ?`+func() string { if c.HideUnrated() { return " OR rating_rank < 0" }; return "" }()+`) AND id IN (
		SELECT `+colA+` FROM `+table+` UNION SELECT `+colB+` FROM `+table+`)`, rank)
	if err != nil {
		logx.Debugf("ceiling tainted read: %v", err)
		return nil
	}
	return tainted
}

// g may be nil: Apply still works, ExcludedTagIDs and TaintedAmong
// return nil.
func NewCeiling(level string, g *Gallery, excludeUnrated ...bool) *Ceiling {
	return &Ceiling{level: level, cx: g, excludeUnrated: len(excludeUnrated) > 0 && excludeUnrated[0]}
}
