package acquisitionstage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// Read-only evidence candidate for existing-image dispositions. It is
// intentionally NOT a publication attempt format, a disposition grant,
// a deletion permission, or a new caller of the diagnostic ingest path.
// Keep held and restore explicit; never infer one from failure of the other.
// First scope: single PNG, no parent/collection, empty client job.
const (
	existingHeld = "held"
	existingRestore = "restore"
)

type existingPublicationTarget struct {
	Kind string
	DB landingAttempt
	PolicySHA256 string
	File acquisitionplan.VerifiedTransferFile
	FileIdentity string
	Media plannedImageFacts
	ImageID int64
	Held *heldDuplicate
	Retained []retainedPath
	Restore *landingAttempt
	StateSHA256 string
}

func sameExistingPublicationTarget(a, b existingPublicationTarget) bool {
	return reflect.DeepEqual(a, b)
}

func observeExistingTarget(ctx context.Context, g *library.Gallery, r record, e SealedEvidence, snapshot acquisitionplan.ApprovedPolicySnapshot, dst *os.Root, kind string, media plannedImageFacts) (existingPublicationTarget, error) {
	var zero existingPublicationTarget
	rows, err := newOnlyRows(e, snapshot)
	if err != nil || len(rows) != 1 || len(e.Files) != 1 || filepath.Ext(e.Files[0].RelativeName) != ".png" || !validPlannedImageFacts(e.Files[0], &media) {
		return zero, ErrStageRefused
	}
	f, row := e.Files[0], rows[0]
	db, err := observedDB(r, g)
	if err != nil {
		return zero, err
	}
	var id int64
	tx, err := g.DB.Read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return zero, ErrStageRecovery
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT id FROM images WHERE sha256=? AND file_size=?`, f.SHA256, f.Size).Scan(&id); err != nil || id <= 0 {
		return zero, ErrStageRefused
	}
	alias := filepath.Join(r.GalleryRoot, finalName(r.Allocation, f))
	for _, q := range []struct{ sql string; args []any }{
		{`SELECT COUNT(*) FROM image_paths WHERE path=?`, []any{alias}},
		{`SELECT COUNT(*) FROM image_sources WHERE ((site=? AND post_id=?) OR url=?) AND image_id<>?`, []any{row.SourceLabel, row.PostID, row.PostURL, id}},
		{`SELECT COUNT(*) FROM image_sources WHERE parent_url=?`, []any{row.PostURL}},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, q.sql, q.args...).Scan(&count); err != nil || count != 0 {
			return zero, ErrStageRefused
		}
	}
	out := existingPublicationTarget{Kind: kind, DB: db, File: f, Media: media, ImageID: id}
	var rel string
	switch kind {
	case existingHeld:
		rel, err = duplicateCanonicalRowMode(ctx, tx, r.GalleryRoot, alias, id, f, 0)
		if err != nil {
			return zero, err
		}
	case existingRestore:
		// The existing restoration observer additionally checks exact
		// rooted absence and the old canonical row and preservation digest.
	default:
		return zero, ErrStageRefused
	}
	if tx.Commit() != nil || ctx.Err() != nil {
		return zero, ErrStageRecovery
	}
	if err := verifyPlannedImageRow(ctx, g, id, f, &media); err != nil {
		return zero, err
	}
	switch kind {
	case existingHeld:
		identity, err := verifyHeldCanonical(ctx, dst, rel, f)
		if err != nil {
			return zero, err
		}
		h := heldDuplicate{PostID: f.ID, ImageID: id, CanonicalPath: rel, CanonicalIdentity: identity, Size: f.Size, SHA256: f.SHA256}
		out.StateSHA256, out.Retained, err = retainedHeldState(ctx, g, r, dst, f, h)
		if err != nil {
			return zero, err
		}
		out.Held = &h
	case existingRestore:
		restore := db
		if err := captureRestoreTarget(ctx, g, r, e, snapshot, dst, &restore); err != nil || restore.RestoreImageID != id {
			return zero, ErrStageRefused
		}
		out.StateSHA256, err = restoreStateDigest(ctx, g, id)
		if err != nil {
			return zero, err
		}
		out.Restore = &restore
	}
	if !validDigest(out.StateSHA256) {
		return zero, ErrStageRecovery
	}
	return out, nil
}

// Require the same exact live evidence on two observations around the
// second authority read. This only detects cooperative changes; the
// future owner must provide actual scanner/writer exclusion.
func (a *Allocator) inspectExistingPublicationTarget(ctx context.Context, grant workerproto.StageAllocation, g *library.Gallery, kind string, extras extrasAuthority) (existingPublicationTarget, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.inspectExistingPublicationTargetLocked(ctx, grant, g, kind, extras)
}

func (a *Allocator) inspectExistingPublicationTargetLocked(ctx context.Context, grant workerproto.StageAllocation, g *library.Gallery, kind string, extras extrasAuthority) (existingPublicationTarget, error) {
	var zero existingPublicationTarget
	if g == nil || g.WatcherRunning() || extras == nil || (kind != existingHeld && kind != existingRestore) {
		return zero, ErrStageRefused
	}
	job, err := extras(ctx, grant)
	if err != nil || !job.Empty() {
		return zero, ErrStageRefused
	}
	r, e, p, src, dst, err := a.publicationRoots(ctx, grant)
	if err != nil {
		return zero, err
	}
	defer p.Close()
	defer src.Close()
	defer dst.Close()
	if len(e.Files) != 1 {
		return zero, ErrStageRefused
	}
	f := e.Files[0]
	observe := func() (existingPublicationTarget, error) {
		if _, err := p.Lstat("attempt"); !errors.Is(err, os.ErrNotExist) {
			return zero, ErrStageRefused
		}
		places, err := inspectPlacements(ctx, grant, e, p, src, dst)
		if err != nil || len(places) != 1 || places[0].State != "prepared" {
			return zero, ErrStageRecovery
		}
		snapshot, err := a.boundPolicyLocked(ctx, grant)
		if err != nil {
			return zero, err
		}
		_, hash, err := acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer, snapshot.Rows)
		if err != nil {
			return zero, ErrStageRecovery
		}
		identity, err := namingFile(ctx, src, f.RelativeName, f, 1)
		if err != nil {
			return zero, err
		}
		media, err := inspectPlannedImage(ctx, src, f, 1)
		if err != nil {
			return zero, err
		}
		got, err := observeExistingTarget(ctx, g, r, e, snapshot, dst, kind, media)
		if err != nil {
			return zero, err
		}
		current, err := namingFile(ctx, src, f.RelativeName, f, 1)
		if err != nil || current != identity {
			return zero, ErrStageRecovery
		}
		got.PolicySHA256, got.FileIdentity = hash, identity
		return got, nil
	}
	before, err := observe()
	if err != nil {
		return zero, err
	}
	job, err = extras(ctx, grant)
	if err != nil || !job.Empty() {
		return zero, ErrStageRefused
	}
	after, err := observe()
	if err != nil || !sameExistingPublicationTarget(before, after) {
		return zero, ErrStageRecovery
	}
	scope, err := a.scope(ctx, grant.Request)
	if err != nil || !matches(r, scope) || g.WatcherRunning() || ctx.Err() != nil || !a.livePublicationRoots(grant, r, p, src, dst) {
		return zero, ErrStageRefused
	}
	return after, nil
}
