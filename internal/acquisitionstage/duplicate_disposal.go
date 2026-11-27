package acquisitionstage

import(
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"io"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/db"
)

// Isolated single-held-duplicate disposal. A synced attempt binds the
// already verified metadata result and the exact independent canonical
// object. No new/restore/mixed batch, no production caller or receipt.
type duplicateAttempt struct{
	Version int `json:"version"`
	MetadataSHA256 string `json:"metadata_sha256"`
	Held heldDuplicate `json:"held"`
}
func readDuplicateAttempt(p *os.Root,e SealedEvidence,result metadataResult)(duplicateAttempt,error){
	var zero duplicateAttempt
	raw,err:=boundedRegularBytes(p,"duplicate-attempt.json",maxSealRecordBytes)
	if err!=nil{return zero,err}
	var d duplicateAttempt
	body,err:=json.Marshal(result)
	if err!=nil||len(e.Files)!=1||len(result.Rows)!=1||json.Unmarshal(raw,&d)!=nil||d.Version!=1||d.MetadataSHA256!=digestBytes(body){return zero,ErrStageRecovery}
	f:=e.Files[0];h:=d.Held
	if h.PostID!=f.ID||h.ImageID!=result.Rows[0].ImageID||h.Size!=f.Size||h.SHA256!=f.SHA256||
		!filepath.IsLocal(h.CanonicalPath)||filepath.Clean(h.CanonicalPath)!=h.CanonicalPath||h.CanonicalPath=="."||
		h.CanonicalIdentity==""||len(h.CanonicalIdentity)>128{return zero,ErrStageRecovery}
	canonical,err:=json.Marshal(d)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return d,nil
}

// Independently observe completed deletion, never infer success from
// a missing file alone. The private publication copy remains intact.
// No mutation or retry on recovery.
func(a *Allocator)observeDuplicateDisposalLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)(heldDuplicate,error){
	var zero heldDuplicate
	if g==nil||g.WatcherRunning(){return zero,ErrStageRefused}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return zero,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	l,err:=readLandingAttempt(p,r)
	if err!=nil||l.NewOnly{return zero,ErrStageRecovery}
	current,err:=observedDB(r,g)
	if err!=nil||current!=l{return zero,ErrStageRecovery}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return zero,err}
	_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	if err!=nil||hash!=m.PolicySHA256{return zero,ErrStageRecovery}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return zero,err}
	d,err:=readDuplicateAttempt(p,e,result)
	if err!=nil{return zero,err}
	rows,err:=selectMappedRows(snapshot,e)
	if err!=nil||len(rows)!=1{return zero,ErrStageRecovery}
	v,err:=verifyMappedRow(ctx,g,m.Targets[0],rows[0],result.Rows[0].Rejected)
	if err!=nil||!sameMetadataObservation(v,result.Rows[0]){return zero,ErrStageRecovery}
	f:=e.Files[0]
	h:=d.Held
	places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
	if err!=nil||len(places)!=1||places[0].State!="absent"{return zero,ErrStageRecovery}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return zero,ErrStageRecovery}
	defer tx.Rollback()
	var count int
	canonical:=filepath.Join(r.GalleryRoot,h.CanonicalPath)
	for _,check:=range []struct{q string;args []any;want int}{
		{`SELECT COUNT(*) FROM images WHERE id=? AND canonical_path=? AND sha256=? AND file_size=? AND is_missing=0`,[]any{h.ImageID,canonical,h.SHA256,h.Size},1},
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND path=? AND is_canonical=1`,[]any{h.ImageID,canonical},1},
		{`SELECT COUNT(*) FROM image_paths WHERE path=?`,[]any{filepath.Join(r.GalleryRoot,finalName(grant,f))},0},
	}{
		if err:=tx.QueryRowContext(ctx,check.q,check.args...).Scan(&count);err!=nil||count!=check.want{return zero,ErrStageRecovery}
	}
	id,err:=verifyHeldCanonical(ctx,dst,h.CanonicalPath,f)
	if err!=nil||id!=h.CanonicalIdentity{return zero,ErrStageRecovery}
	if err:=tx.Commit();err!=nil{return zero,ErrStageRecovery}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s)||g.WatcherRunning(){return zero,ErrStageRefused}
	current,err=observedDB(r,g)
	if err!=nil||current!=l||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRecovery}
	places,err=inspectPlacements(ctx,grant,e,p,src,dst)
	if err!=nil||len(places)!=1||places[0].State!="absent"{return zero,ErrStageRecovery}
	id,err=verifyHeldCanonical(ctx,dst,h.CanonicalPath,f)
	if err!=nil||id!=h.CanonicalIdentity{return zero,ErrStageRecovery}
	return h,nil
}
func(a *Allocator)observeDuplicateDisposal(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)(heldDuplicate,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeDuplicateDisposalLocked(ctx,grant,g)
}
// Compare retained handles to the current owner-selected roots. Point-in-
// time under cooperative owner exclusion, not a hostile-writer defense.
func(a *Allocator)livePublicationRoots(grant workerproto.StageAllocation,r record,p,src,dst *os.Root)bool{
	if !a.live(){return false}
	base:=filepath.Join(a.path,grant.Request.OperationID,"publication")
	for _,v:=range []struct{path string;root *os.Root}{{base,p},{filepath.Join(base,"media"),src},{r.GalleryRoot,dst}}{
		_,current,err:=directory(v.path)
		if err!=nil{return false}
		opened,err:=v.root.Stat(".")
		if err!=nil||!os.SameFile(current,opened){return false}
	}
	return true
}
func(a *Allocator)disposeHeldDuplicate(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)(heldDuplicate,error){
	var zero heldDuplicate
	a.mu.Lock();defer a.mu.Unlock()
	held,err:=a.observeHeldDuplicatesLocked(ctx,grant,g)
	if err!=nil||len(held)!=1{return zero,ErrStageRecovery}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if _,err:=p.Lstat("duplicate-attempt.json");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	l,err:=readLandingAttempt(p,r)
	if err!=nil{return zero,err}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return zero,err}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return zero,err}
	raw,err:=json.Marshal(result)
	if err!=nil{return zero,ErrStageRecovery}
	d:=duplicateAttempt{Version:1,MetadataSHA256:digestBytes(raw),Held:held[0]}
	body,err:=json.Marshal(d)
	if err!=nil||len(body)>maxSealRecordBytes{return zero,ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"duplicate-attempt.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	again,err:=a.observeHeldDuplicatesLocked(ctx,grant,g)
	if err!=nil||len(again)!=1||again[0]!=d.Held{return zero,ErrStageRecovery}
	f:=e.Files[0]
	name:=finalName(grant,f)
	if !a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRecovery}
	places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
	if err!=nil||len(places)!=1||places[0].State!="placed"{return zero,ErrStageRecovery}
	canonicalID,err:=verifyHeldCanonical(ctx,dst,d.Held.CanonicalPath,f)
	if err!=nil||canonicalID!=d.Held.CanonicalIdentity{return zero,ErrStageRecovery}
	// Only the deterministic operation-owned second link is removed.
	// Filesystem first; a failed DB delete leaves an uncertain attempt
	// and retained private bytes. No automated recovery mutation.
	if err:=dst.Remove(name);err!=nil{return zero,ErrStageRecovery}
	syncDirectory(dst)
	if err:=readChecked(ctx,src,f.RelativeName,f.Size,f.SHA256,io.Discard);err!=nil{return zero,err}
	id,err:=verifyHeldCanonical(ctx,dst,d.Held.CanonicalPath,f)
	if err!=nil||id!=d.Held.CanonicalIdentity{return zero,ErrStageRecovery}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s)||g.WatcherRunning(){return zero,ErrStageRefused}
	current,err:=observedDB(r,g)
	if err!=nil||current!=l||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRecovery}
	if _,err:=dst.Lstat(name);!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	err=g.Edit(func(database *db.DB)error{
		res,err:=database.Write.ExecContext(ctx,`DELETE FROM image_paths WHERE image_id=? AND path=? AND is_canonical=0
			AND EXISTS (SELECT 1 FROM images WHERE id=? AND canonical_path=? AND sha256=? AND is_missing=0)`,
			d.Held.ImageID,filepath.Join(r.GalleryRoot,name),d.Held.ImageID,filepath.Join(r.GalleryRoot,d.Held.CanonicalPath),f.SHA256)
		if err!=nil{return err}
		n,err:=res.RowsAffected()
		if err!=nil||n!=1{return ErrStageRecovery}
		return nil
	})
	if err!=nil{return zero,ErrStageRecovery}
	return a.observeDuplicateDisposalLocked(ctx,grant,g)
}
