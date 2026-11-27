package acquisitionstage

import(
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"io"
	_ "image/png"
	"os"
	"path/filepath"
	"slices"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/models"
)

// The first terminal receipt is deliberately restricted to an isolated
// new PNG-only import with complete explicit source detail capture,
// every approved post returned, no existing bytes/source/relations,
// and no job extras. It is NOT a general production receipt. The
// caller must hold exclusive gallery lifetime and scan exclusion.
const newReceiptKind = "isolated_new_png_v1"
const newReceiptOrigin = "managed-isolated"
type newReceiptRow struct{
	PostID string `json:"post_id"`
	ImageID int64 `json:"image_id"`
	Name string `json:"name"`
	Size int64 `json:"size"`
	SHA256 string `json:"sha256"`
}
type newReceipt struct{
	Version int `json:"version"`
	Kind string `json:"kind"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	GalleryIdentity string `json:"gallery_identity"`
	MetadataResultSHA256 string `json:"metadata_result_sha256"`
	Rows []newReceiptRow `json:"rows"`
}
func newOnlyRows(e SealedEvidence,snapshot acquisitionplan.ApprovedPolicySnapshot)([]acquisitionplan.ApprovedPolicyRow,error){
	return newOnlyRowsMode(e,snapshot,false)
}
// Explicit ordered collections may land for separate phase diagnostics,
// but the old new-only terminal receipt and restore path remain unchanged.
func newOnlyRowsMode(e SealedEvidence,snapshot acquisitionplan.ApprovedPolicySnapshot,allowCollection bool)([]acquisitionplan.ApprovedPolicyRow,error){
	if len(e.Files)==0||len(e.Files)!=len(snapshot.Rows){return nil,ErrStageRefused}
	rows,err:=selectMappedRows(snapshot,e)
	if err!=nil{return nil,err}
	seen:=map[string]bool{}
	urls:=map[string]bool{}
	for i,r:=range rows{
		if r.SourceLabel==models.TagSourceMonbooru||r.Provenance==nil||r.Provenance.ParentURL!=""||(!allowCollection&&(r.Provenance.Collection!=""||r.Provenance.CollectionOrder!=0))||
			filepath.Ext(e.Files[i].RelativeName)!=".png"||seen[e.Files[i].SHA256]||urls[r.PostURL]{return nil,ErrStageRefused}
		seen[e.Files[i].SHA256]=true;urls[r.PostURL]=true
	}
	return rows,nil
}
func verifyNewOnlyEligibility(ctx context.Context,g *library.Gallery,e SealedEvidence,snapshot acquisitionplan.ApprovedPolicySnapshot)error{
	rows,err:=newOnlyRowsMode(e,snapshot,true)
	if err!=nil{return err}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return ErrStageRecovery}
	defer tx.Rollback()
	for i,row:=range rows{
		var n int
		if err:=tx.QueryRowContext(ctx,`SELECT COUNT(*) FROM images WHERE sha256=?`,e.Files[i].SHA256).Scan(&n);err!=nil||n!=0{return ErrStageRefused}
		if err:=tx.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_sources WHERE (site=? AND post_id=?) OR url=? OR parent_url=?`,row.SourceLabel,row.PostID,row.PostURL,row.PostURL).Scan(&n);err!=nil||n!=0{return ErrStageRefused}
	}
	if err:=tx.Commit();err!=nil{return ErrStageRecovery}
	return nil
}

// Snapshot library-derived tag ownership immediately after actual
// landing, before any mapped source writes. Ingestion legitimately
// generates local meta tags (e.g. lowres); never conflate them with the
// approved remote source or accept unrelated user/source attribution.
func readNewDerivedBaseline(ctx context.Context,g *library.Gallery,id int64)([]int64,error){
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return nil,ErrStageRecovery}
	defer tx.Rollback()
	rows,err:=tx.QueryContext(ctx,`SELECT ts.tag_id FROM image_tag_sources ts JOIN image_tags it
		ON it.image_id=ts.image_id AND it.tag_id=ts.tag_id WHERE ts.image_id=? AND ts.source=? AND it.stale=0 ORDER BY ts.tag_id`,id,models.TagSourceMonbooru)
	if err!=nil{return nil,ErrStageRecovery}
	var out []int64
	for rows.Next(){
		var tag int64
		if rows.Scan(&tag)!=nil||tag<=0||(len(out)>0&&tag<=out[len(out)-1])||len(out)>=8192{
			rows.Close();return nil,ErrStageRecovery
		}
		out=append(out,tag)
	}
	err=rows.Err();rows.Close()
	if err!=nil{return nil,ErrStageRecovery}
	for _,q:=range []string{`SELECT COUNT(*) FROM image_tag_sources WHERE image_id=?`,`SELECT COUNT(*) FROM image_tags WHERE image_id=? AND is_implied=0`}{
		var count int
		if err:=tx.QueryRowContext(ctx,q,id).Scan(&count);err!=nil||count!=len(out){return nil,ErrStageRecovery}
	}
	if err:=tx.Commit();err!=nil{return nil,ErrStageRecovery}
	return out,nil
}

func verifyNewDerivedBaseline(ctx context.Context,tx *sql.Tx,id int64,expected []int64)error{
	rows,err:=tx.QueryContext(ctx,`SELECT ts.tag_id FROM image_tag_sources ts JOIN image_tags it
		ON it.image_id=ts.image_id AND it.tag_id=ts.tag_id WHERE ts.image_id=? AND ts.source=? AND it.stale=0 ORDER BY ts.tag_id`,id,models.TagSourceMonbooru)
	if err!=nil{return ErrStageRecovery}
	defer rows.Close()
	i:=0
	for rows.Next(){
		var tag int64
		if i>=len(expected)||rows.Scan(&tag)!=nil||tag!=expected[i]{return ErrStageRecovery}
		i++
	}
	if rows.Err()!=nil||i!=len(expected){return ErrStageRecovery}
	return nil
}

// Actual existing ingestion, not a simulated database mutation.
// The durable new-only attempt is recorded before any ingest.
// Never retry this routine after an attempt even if no image row exists.
func(a *Allocator)landNewPrepared(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	a.mu.Lock();defer a.mu.Unlock()
	if err:=a.beginLandingLocked(ctx,grant,g,true);err!=nil{return err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	landing,err:=readLandingAttempt(p,r)
	if err!=nil||!landing.NewOnly{return ErrStageRecovery}
	for _,f:=range e.Files{
		s,err:=a.scope(ctx,grant.Request)
		if err!=nil||!matches(r,s)||g.WatcherRunning(){return ErrStageRefused}
		current,err:=observedDB(r,g)
		if err!=nil||!sameLandingDB(current,landing){return ErrStageRecovery}
		name:=finalName(grant,f)
		if err:=readCheckedLinks(ctx,dst,name,f.Size,f.SHA256,io.Discard,2);err!=nil{return err}
		path:=filepath.Join(r.GalleryRoot,name)
		res,err:=g.LandInPlace(ctx,path,library.LandSpec{Origin:newReceiptOrigin,MaxBytes:f.Size,Check:func(path,_ string)error{
			f,err:=os.Open(path)
			if err!=nil{return err}
			defer f.Close()
			cfg,format,err:=image.DecodeConfig(f)
			if err!=nil||format!="png"||cfg.Width<=0||cfg.Height<=0{return ErrStageRefused}
			return nil
		}})
		if err!=nil||res.Image==nil||res.Duplicate||res.Dropped||res.Image.CanonicalPath!=path{return ErrStageRecovery}
	}
	observed,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||len(observed)!=len(e.Files){return ErrStageRecovery}
	for _,v:=range observed{if !v.Canonical||v.State!="observed_path"{return ErrStageRecovery}}
	return nil
}
func expectedNewReceipt(r record,e SealedEvidence,m metadataAttempt,result metadataResult)(newReceipt,error){
	var zero newReceipt
	if !m.Landing.NewOnly||len(e.Files)==0||len(result.Rows)!=len(e.Files){return zero,ErrStageRecovery}
	raw,err:=json.Marshal(result)
	if err!=nil||len(raw)>maxMetadataResultBytes{return zero,ErrStageRecovery}
	receipt:=newReceipt{Version:1,Kind:newReceiptKind,Allocation:r.Allocation,GalleryIdentity:r.GalleryIdentity,MetadataResultSHA256:digestBytes(raw),Rows:make([]newReceiptRow,0,len(e.Files))}
	for i,f:=range e.Files{
		v:=result.Rows[i]
		if v.PostID!=f.ID||m.Targets[i].PriorRating!=""{return zero,ErrStageRecovery}
		receipt.Rows=append(receipt.Rows,newReceiptRow{PostID:f.ID,ImageID:v.ImageID,Name:finalName(r.Allocation,f),Size:f.Size,SHA256:f.SHA256})
	}
	return receipt,nil
}
func readNewReceipt(p *os.Root,r record,e SealedEvidence,m metadataAttempt,result metadataResult)(newReceipt,error){
	var zero newReceipt
	expected,err:=expectedNewReceipt(r,e,m,result)
	if err!=nil{return zero,err}
	body,err:=boundedRegularBytes(p,"new-receipt.json",maxSealRecordBytes)
	if err!=nil{return zero,err}
	canonical,err:=json.Marshal(expected)
	if err!=nil||!bytes.Equal(body,canonical){return zero,ErrStageRecovery}
	return expected,nil
}

// Reconcile exact path, bytes, database, source membership and tag
// attribution under the same local owner. Require the complete mapped
// result, not merely an existing row or a successful service call.
// No mutation on observation, and no cleanup permission.
// Verify exact new-only metadata independently of the current canonical path.
// Callers must separately prove the file and canonical path row.
func verifyNewMetadataState(ctx context.Context,g *library.Gallery,rows []acquisitionplan.ApprovedPolicyRow,m metadataAttempt,result metadataResult)error{
	return verifyNewMetadataStateWithClient(ctx,g,rows,m,result,nil)
}

// Client ownership is an independently proven additive ledger, never
// a replacement for source or locally derived ownership.
func verifyNewMetadataStateWithClient(ctx context.Context,g *library.Gallery,rows []acquisitionplan.ApprovedPolicyRow,m metadataAttempt,result metadataResult,client *clientTagState)error{
	if !m.Landing.NewOnly||len(rows)==0||len(rows)!=len(m.Targets)||len(rows)!=len(result.Rows)||
		(client!=nil&&(len(rows)!=1||client.Via==""||client.Via==rows[0].SourceLabel||client.Via==models.TagSourceMonbooru)){return ErrStageRecovery}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return ErrStageRecovery}
	defer tx.Rollback()
	for i,row:=range rows{
		v:=result.Rows[i]
		in,md5,err:=normalizedMappedSource(row)
		if err!=nil||verifySourceClaimsMode(ctx,tx,v.ImageID,in,md5,true)!=nil{return ErrStageRecovery}
		derived:=m.Targets[i].DerivedTags
		if err:=verifyNewDerivedBaseline(ctx,tx,v.ImageID,derived);err!=nil{return err}
		direct:=map[int64]bool{}
		for _,id:=range derived{direct[id]=true}
		for _,id:=range v.TagIDs{direct[id]=true}
		clientCount:=0
		if client!=nil{
			clientCount=len(client.TagIDs)
			for _,id:=range client.TagIDs{
				direct[id]=true
				var count int
				if err:=tx.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_tag_sources ts JOIN image_tags it ON it.image_id=ts.image_id AND it.tag_id=ts.tag_id WHERE ts.image_id=? AND ts.tag_id=? AND ts.source=? AND it.stale=0 AND it.is_implied=0 AND it.is_auto=0`,v.ImageID,id,client.Via).Scan(&count);err!=nil||count!=1{return ErrStageRecovery}
			}
		}
		var n int
		// New-only: no extra origins or aliases, no unexpected active
		// attribution to this source, and no omitted parent/collection.
		for _,check:=range []struct{query string;args []any;want int}{
			{`SELECT COUNT(*) FROM images WHERE id=? AND origin=? AND source=? AND url=?`,[]any{v.ImageID,newReceiptOrigin,row.SourceLabel,row.PostURL},1},
			{`SELECT COUNT(*) FROM image_paths WHERE image_id=?`,[]any{v.ImageID},1},
			{`SELECT COUNT(*) FROM image_sources WHERE image_id=?`,[]any{v.ImageID},1},
			{`SELECT COUNT(*) FROM image_tag_sources WHERE image_id=?`,[]any{v.ImageID},len(v.TagIDs)+len(derived)+clientCount},
			{`SELECT COUNT(*) FROM image_tag_sources ts JOIN image_tags it ON it.image_id=ts.image_id AND it.tag_id=ts.tag_id WHERE ts.image_id=? AND ts.source=? AND it.stale=0`,[]any{v.ImageID,row.SourceLabel},len(v.TagIDs)},
			{`SELECT COUNT(*) FROM image_tags WHERE image_id=? AND is_implied=0`,[]any{v.ImageID},len(direct)},
			{`SELECT COUNT(*) FROM image_collections WHERE image_id=?`,[]any{v.ImageID},0},
			{`SELECT COUNT(*) FROM images WHERE id=? AND COALESCE(series,'')='' AND series_order IS NULL`,[]any{v.ImageID},1},
			{`SELECT COUNT(*) FROM derivative_edges WHERE source_image_id=? OR derivative_image_id=?`,[]any{v.ImageID,v.ImageID},0},
			{`SELECT COUNT(*) FROM image_sources WHERE parent_url=?`,[]any{row.PostURL},0},
		}{
			if err:=tx.QueryRowContext(ctx,check.query,check.args...).Scan(&n);err!=nil||n!=check.want{return ErrStageRecovery}
		}
	}
	if err:=tx.Commit();err!=nil{return ErrStageRecovery}
	return nil
}

func(a *Allocator)newReceiptLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,write bool)(newReceipt,error){
	var zero newReceipt
	if g==nil||g.WatcherRunning(){return zero,ErrStageRefused}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return zero,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	// The restricted receipt is not authority to skip an uncertain relocation.
	if _,err:=p.Lstat("naming-attempt.json");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	rows,err:=newOnlyRows(e,snapshot)
	if err!=nil{return zero,err}
	l,err:=readLandingAttempt(p,r)
	if err!=nil||!l.NewOnly{return zero,ErrStageRecovery}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return zero,err}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return zero,err}
	observed,err:=a.observeMappedMetadataLocked(ctx,grant,g)
	if err!=nil||len(observed)!=len(rows){return zero,ErrStageRecovery}
	landed,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||len(landed)!=len(rows){return zero,ErrStageRecovery}
	for i,v:=range observed{
		if !landed[i].Canonical||landed[i].ImageID!=v.ImageID||!sameMetadataObservation(v,result.Rows[i]){return zero,ErrStageRecovery}
	}
	if err:=verifyNewMetadataState(ctx,g,rows,m,result);err!=nil{return zero,err}
	current,err:=observedDB(r,g)
	if err!=nil||!sameLandingDB(current,l){return zero,ErrStageRecovery}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s){return zero,ErrStageRefused}
	expected,err:=expectedNewReceipt(r,e,m,result)
	if err!=nil{return zero,err}
	if !write{return readNewReceipt(p,r,e,m,result)}
	if _,err:=p.Lstat("new-receipt.json");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	body,err:=json.Marshal(expected)
	if err!=nil||len(body)>maxSealRecordBytes{return zero,ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"new-receipt.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	s,err=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s){return zero,ErrStageRefused}
	stored,err:=readNewReceipt(p,r,e,m,result)
	if err!=nil||!slices.Equal(stored.Rows,expected.Rows){return zero,ErrStageRecovery}
	// A saved receipt does not exempt the writer from the same
	// read-only reconciliation required after a lost acknowledgement.
	return a.newReceiptLocked(ctx,grant,g,false)
}
func(a *Allocator)finishNewReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)(newReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.newReceiptLocked(ctx,grant,g,true)
}
func(a *Allocator)observeNewReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)(newReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.newReceiptLocked(ctx,grant,g,false)
}
