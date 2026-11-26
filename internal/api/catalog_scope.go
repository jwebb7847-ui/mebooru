package api

import (
	"context"
	"errors"
	"sync/atomic"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jwebb7847-ui/mebooru/shared/contract"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

var errCatalogScope = errors.New("invalid managed catalog scope")

// CatalogScope is a library-owned, revocable read-only grant for one
// session. It pins live gallery objects, not durable gallery identities.
// It cannot grant image access, mutation, or network work. Construct only
// from trusted library authorization, never from a worker request.
type CatalogScope struct {
	owner *Handler
	discover bool
	galleries map[string]catalogGrant
	revoked atomic.Bool
}

type catalogGrant struct {
	gallery *library.Gallery
	operations map[workerproto.Operation]bool
}

// NewCatalogScope snapshots explicit operation grants under the gallery
// lifetime lock. A replaced/reopened/renamed gallery requires a new
// authorized scope; a matching display name alone is not sufficient.
func (h *Handler) NewCatalogScope(ctx context.Context, discover bool, grants map[string][]workerproto.Operation) (*CatalogScope, error) {
	if h == nil || h.lock == nil || h.resolver == nil || h.cfg == nil ||
		len(grants) > workerproto.MaxCatalogEntries || ctx.Err() != nil {
		return nil, errCatalogScope
	}
	unlock := h.lock()
	defer unlock()
	configured:=h.configuredCatalogNames()
	if configured==nil{return nil,errCatalogScope}
	s := &CatalogScope{owner: h, discover: discover, galleries: make(map[string]catalogGrant, len(grants))}
	for name, operations := range grants {
		if !utf8.ValidString(name)||strings.TrimSpace(name)==""||strings.ContainsFunc(name,unicode.IsControl)||len(name) > workerproto.MaxCatalogNameBytes || configured[name]!=1 ||
			len(operations) == 0 || len(operations) > 3 || ctx.Err() != nil {
			return nil, errCatalogScope
		}
		g, ok := h.resolver(name)
		if !ok || g == nil || g.Closed() || g.Name != name { return nil, errCatalogScope }
		entry := catalogGrant{gallery: g, operations: make(map[workerproto.Operation]bool, len(operations))}
		for _, op := range operations {
			switch op {
			case workerproto.GalleryList:
				if !discover { return nil, errCatalogScope }
			case workerproto.CategoryList, workerproto.ResolveTags:
			default:
				return nil, errCatalogScope
			}
			if entry.operations[op] { return nil, errCatalogScope }
			entry.operations[op] = true
		}
		s.galleries[name] = entry
	}
	if ctx.Err() != nil { return nil, errCatalogScope }
	return s, nil
}

// Revoke is irreversible. It closes future admission and is checked again
// before a handler returns a successful result. Already returned results
// cannot be recalled. It is not a session shutdown or a DB cancellation.
func (s *CatalogScope) Revoke() {
	if s != nil { s.revoked.Store(true) }
}

func (s *CatalogScope) allows(ctx context.Context, op workerproto.Operation, name string) bool {
	if s == nil || s.revoked.Load() || ctx.Err() != nil { return false }
	if name == "" { return op == workerproto.GalleryList && s.discover }
	g, ok := s.galleries[name]
	return ok && g.operations[op]
}

// verifyLive is invoked only under the owning handler's lifetime lock.
// Discovery must not silently omit a granted gallery that disappeared.
func (s *CatalogScope) verifyLive(ctx context.Context, op workerproto.Operation, name string) bool {
	if !s.allows(ctx, op, name) { return false }
	configured:=s.owner.configuredCatalogNames()
	if configured==nil{return false}
	check := func(n string, g catalogGrant) bool {
		if configured[n]!=1{return false}
		current, ok := s.owner.resolver(n)
		return ok && current != nil && current == g.gallery && !current.Closed() && current.Name == n
	}
	if name == "" {
		for n, g := range s.galleries {
			if g.operations[workerproto.GalleryList] && !check(n, g) { return false }
		}
		return true
	}
	return check(name, s.galleries[name])
}


// Config membership is distinct from a resolver pointer. A stale resolver
// must not keep a deleted gallery authorized, and discovery must not silently
// omit a granted gallery missing from the current configuration snapshot.
func(h *Handler) configuredCatalogNames()map[string]int{
	cfg:=h.cfg()
	if cfg==nil{return nil}
	names:=make(map[string]int,len(cfg.Galleries))
	for _,g:=range cfg.Galleries{names[g.Name]++}
	return names
}

// Verify the result set itself under the same gallery lifetime lock.
// This also detects a transient config/list disagreement between snapshots.
func(s *CatalogScope)verifyDiscovery(ctx context.Context,list []contract.Gallery)bool{
	if !s.allows(ctx,workerproto.GalleryList,""){return false}
	expected:=make(map[string]bool)
	for name,grant:=range s.galleries{
		if grant.operations[workerproto.GalleryList]{expected[name]=true}
	}
	for _,g:=range list{
		if !expected[g.Name]{return false}
		delete(expected,g.Name)
	}
	return len(expected)==0
}
