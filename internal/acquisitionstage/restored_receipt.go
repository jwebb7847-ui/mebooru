package acquisitionstage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

const restoredReceiptKind = "isolated_named_restored_png_v1"
const maxRestoredReceipt = 128 << 10

// The pre-restoration invariant contains operator-owned fields that the
// qualified restoration and no-new-collection source merge must preserve.
// Source-owned values can legitimately change on refetch, so they are
// independently reconciled by mapped metadata instead of included here.
func restorePreservationDigest(ctx context.Context,tx *sql.Tx,id int64)(string,error){
	return boundedStateDigest(ctx,tx,[]stateQuery{
		{`SELECT id,origin,note,original_source,is_favorited,is_inbox,series,series_order FROM images WHERE id=?`,[]any{id}},
		{`SELECT * FROM image_collections WHERE image_id=? ORDER BY name COLLATE BINARY LIMIT 8193`,[]any{id}},
		{`SELECT * FROM image_annotations WHERE image_id=? AND manual=1 ORDER BY id LIMIT 8193`,[]any{id}},
	},nil)
}

// Exact post-merge state, normalized ONLY for the two path columns in
// the image row and the canonical path row's path, plus their generated
// basename_lower columns (SQLite recomputes them from the path). All path-row IDs,
// old missing alias rows, origin, ratings, notes, source ownership,
// memberships and relationship edges remain part of the digest.
func restoreStateDigest(ctx context.Context,g *library.Gallery,id int64)(string,error){
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return "",ErrStageRecovery}
	defer tx.Rollback()
	state,err:=boundedStateDigest(ctx,tx,[]stateQuery{
		{`SELECT * FROM images WHERE id=?`,[]any{id}},
		{`SELECT * FROM image_sources WHERE image_id=? ORDER BY site COLLATE BINARY,post_id COLLATE BINARY LIMIT 8193`,[]any{id}},
		{`SELECT * FROM image_annotations WHERE image_id=? ORDER BY id LIMIT 8193`,[]any{id}},
		{`SELECT * FROM image_tags WHERE image_id=? ORDER BY tag_id LIMIT 8193`,[]any{id}},
		{`SELECT * FROM image_tag_sources WHERE image_id=? ORDER BY tag_id,source COLLATE BINARY LIMIT 8193`,[]any{id}},
		{`SELECT * FROM image_collections WHERE image_id=? ORDER BY name COLLATE BINARY LIMIT 8193`,[]any{id}},
		{`SELECT * FROM derivative_edges WHERE source_image_id=? OR derivative_image_id=? ORDER BY source_image_id,derivative_image_id LIMIT 8193`,[]any{id,id}},
		{`SELECT * FROM image_paths WHERE image_id=? ORDER BY id LIMIT 8193`,[]any{id}},
	},func(query string,cols []string,vals []any){
		if strings.HasPrefix(query,"SELECT * FROM images "){
			for i,col:=range cols{if col=="canonical_path"||col=="folder_path"||col=="basename_lower"{vals[i]=nil}}
		}
		if strings.HasPrefix(query,"SELECT * FROM image_paths "){
			canonical:=false
			for i,col:=range cols{if col=="is_canonical"{v,ok:=vals[i].(int64);canonical=ok&&v==1}}
			if canonical{for i,col:=range cols{if col=="path"||col=="basename_lower"{vals[i]=nil}}}
		}
	})
	if err!=nil{return "",err}
	if tx.Commit()!=nil||ctx.Err()!=nil{return "",ErrStageRecovery}
	return state,nil
}

// Restoration differs from new-only placement: the preexisting image
// origin must not be overwritten, and a marked-missing old row remains
// a noncanonical missing alias. Reconcile its exact persisted form.
func restoredNamingRows(ctx context.Context,g *library.Gallery,r record,f acquisitionplan.VerifiedTransferFile,id int64,rel,absent string,l landingAttempt)(int64,error){
	if id!=l.RestoreImageID||id<=0||l.NewOnly||l.RestorePathID<=0||!validDigest(l.RestorePreservationSHA256)||strings.EqualFold(rel,l.RestorePath)||strings.EqualFold(absent,l.RestorePath){return 0,ErrStageRecovery}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return 0,ErrStageRecovery}
	defer tx.Rollback()
	path:=filepath.Join(r.GalleryRoot,rel)
	var pid int64
	if err:=tx.QueryRowContext(ctx,`SELECT ip.id FROM image_paths ip JOIN images i ON i.id=ip.image_id
		WHERE i.id=? AND i.sha256=? AND i.file_size=? AND i.is_missing=0 AND i.canonical_path=?
		AND i.folder_path=? AND ip.path=? AND ip.is_canonical=1
		AND i.basename_lower=lower(basename(i.canonical_path)) AND ip.basename_lower=lower(basename(ip.path))`,id,f.SHA256,f.Size,path,
		gallery.FolderPath(r.GalleryRoot,path),path).Scan(&pid);err!=nil||pid<=0{return 0,ErrStageRecovery}
	wantOld,wantTotal:=0,1
	if l.RestoreMarkedMissing{wantOld,wantTotal=1,2}
	for _,q:=range []struct{sql string;args []any;want int}{
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=?`,[]any{id},wantTotal},
		{`SELECT COUNT(*) FROM image_paths WHERE path=?`,[]any{filepath.Join(r.GalleryRoot,absent)},0},
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND is_canonical=1`,[]any{id},1},
		{`SELECT COUNT(*) FROM image_paths WHERE path=? AND image_id=? AND is_canonical=0`,[]any{filepath.Join(r.GalleryRoot,l.RestorePath),id},wantOld},
		{`SELECT COUNT(*) FROM image_paths WHERE path=?`,[]any{filepath.Join(r.GalleryRoot,l.RestorePath)},wantOld},
	}{
		var n int
		if err:=tx.QueryRowContext(ctx,q.sql,q.args...).Scan(&n);err!=nil||n!=q.want{return 0,ErrStageRecovery}
	}
	if l.RestoreMarkedMissing{
		var prior int64
		if err:=tx.QueryRowContext(ctx,`SELECT id FROM image_paths WHERE image_id=? AND path=? AND is_canonical=0`,id,filepath.Join(r.GalleryRoot,l.RestorePath)).Scan(&prior);err!=nil||prior!=l.RestorePathID{return 0,ErrStageRecovery}
	}
	preserved,err:=restorePreservationDigest(ctx,tx,id)
	if err!=nil||preserved!=l.RestorePreservationSHA256||tx.Commit()!=nil||ctx.Err()!=nil{return 0,ErrStageRecovery}
	return pid,nil
}

type restoredReceipt struct{
	Version int `json:"version"`
	Kind string `json:"kind"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	GalleryIdentity string `json:"gallery_identity"`
	PolicySHA256 string `json:"policy_sha256"`
	NamingSHA256 string `json:"naming_sha256"`
	RetainedStateSHA256 string `json:"retained_state_sha256"`
	OldPath string `json:"old_path"`
	OldPathID int64 `json:"old_path_id"`
	PreservationSHA256 string `json:"preservation_sha256"`
	OldMarkedMissing bool `json:"old_marked_missing"`
	PriorRating string `json:"prior_rating"`
	Row newReceiptRow `json:"row"`
	PathID int64 `json:"path_id"`
	FileIdentity string `json:"file_identity"`
}

func expectedRestoredReceipt(p *os.Root,r record,e SealedEvidence,l landingAttempt,m metadataAttempt,result metadataResult)(restoredReceipt,error){
	var zero restoredReceipt
	if l.NewOnly||l.RestoreImageID<=0||l.RestorePathID<=0||!validDigest(l.RestorePreservationSHA256)||len(e.Files)!=1||len(result.Rows)!=1||len(m.Targets)!=1||
		m.Targets[0].ImageID!=l.RestoreImageID||m.Targets[0].PriorRating!=l.RestoreRating{return zero,ErrStageRecovery}
	if err:=validateNamingRecords(p,r,e,l,result,true);err!=nil{return zero,err}
	n,_,err:=readNamingAttempt(p,r,e,result)
	if err!=nil{return zero,err}
	raw,err:=boundedRegularBytes(p,"naming-result.json",maxNamingRecord)
	if err!=nil{return zero,err}
	f:=e.Files[0]
	return restoredReceipt{Version:1,Kind:restoredReceiptKind,Allocation:r.Allocation,GalleryIdentity:r.GalleryIdentity,
		PolicySHA256:m.PolicySHA256,NamingSHA256:digestBytes(raw),RetainedStateSHA256:n.RestoreStateSHA256,
		OldPath:l.RestorePath,OldPathID:l.RestorePathID,PreservationSHA256:l.RestorePreservationSHA256,OldMarkedMissing:l.RestoreMarkedMissing,PriorRating:l.RestoreRating,
		Row:newReceiptRow{PostID:f.ID,ImageID:l.RestoreImageID,Name:n.Final,Size:f.Size,SHA256:f.SHA256},
		PathID:n.PathID,FileIdentity:n.FileIdentity},nil
}
func readRestoredReceipt(p *os.Root,r record,e SealedEvidence,l landingAttempt,m metadataAttempt,result metadataResult)(restoredReceipt,error){
	var zero restoredReceipt
	want,err:=expectedRestoredReceipt(p,r,e,l,m,result)
	if err!=nil{return zero,err}
	raw,err:=boundedRegularBytes(p,"restored-receipt.json",maxRestoredReceipt)
	canonical,marshalErr:=json.Marshal(want)
	if err!=nil||marshalErr!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return want,nil
}
func(a *Allocator)applyRestoredNaming(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	if extras==nil{return ErrStageRefused}
	return a.applyNamingModeLocked(ctx,grant,g,naming,extras)
}
func(a *Allocator)restoredReceiptLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,write bool)(restoredReceipt,error){
	var zero restoredReceipt
	if extras==nil{return zero,ErrStageRefused}
	if err:=a.observeNamingModeLocked(ctx,grant,g,naming,nil,extras);err!=nil{return zero,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	l,err:=readLandingAttempt(p,r)
	if err!=nil{return zero,err}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return zero,err}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return zero,err}
	want,err:=expectedRestoredReceipt(p,r,e,l,m,result)
	if err!=nil{return zero,err}
	job,err:=extras(ctx,grant)
	s,scopeErr:=a.scope(ctx,grant.Request)
	current,dbErr:=observedDB(r,g)
	if err!=nil||!job.Empty()||scopeErr!=nil||!matches(r,s)||dbErr!=nil||!sameLandingDB(current,l)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
	if !write{return readRestoredReceipt(p,r,e,l,m,result)}
	if _,err:=p.Lstat("restored-receipt.json");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	body,err:=json.Marshal(want)
	if err!=nil||len(body)>maxRestoredReceipt{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"restored-receipt.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	return a.restoredReceiptLocked(ctx,grant,g,naming,extras,false)
}
func(a *Allocator)finishRestoredReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(restoredReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.restoredReceiptLocked(ctx,grant,g,naming,extras,true)
}
func(a *Allocator)observeRestoredReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(restoredReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.restoredReceiptLocked(ctx,grant,g,naming,extras,false)
}
