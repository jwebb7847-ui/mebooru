package acquisitionstage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

const maxHeldComposition = 512 << 10
const maxHeldReceipt = 128 << 10
const maxHeldStateBytes = 2 << 20
const maxHeldAliases = 64
const heldReceiptKind = "isolated_held_duplicate_v1"

type retainedPath struct {
	ID int64 `json:"id"`
	Relative string `json:"relative"`
	Canonical bool `json:"canonical"`
	Identity string `json:"identity"`
}
type heldComposition struct {
	Version int `json:"version"`
	Held heldDuplicate `json:"held"`
	MetadataSHA256 string `json:"metadata_sha256"`
	RelationshipSHA256 string `json:"relationship_sha256"`
	CollectionSHA256 string `json:"collection_sha256,omitempty"`
	RetainedStateSHA256 string `json:"retained_state_sha256"`
	Retained []retainedPath `json:"retained"`
}
type heldReceipt struct {
	Version int `json:"version"`
	Kind string `json:"kind"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	GalleryIdentity string `json:"gallery_identity"`
	PolicySHA256 string `json:"policy_sha256"`
	CompositionSHA256 string `json:"composition_sha256"`
	DisposalSHA256 string `json:"disposal_sha256"`
	Held heldDuplicate `json:"held"`
}
func heldCompositionDigest(p *os.Root)(string,error){
	if _,err:=p.Lstat("held-composition.json");errors.Is(err,os.ErrNotExist){return "",nil}else if err!=nil{return "",ErrStageRecovery}
	raw,err:=boundedRegularBytes(p,"held-composition.json",maxHeldComposition)
	if err!=nil{return "",err}
	return digestBytes(raw),nil
}
func readHeldComposition(p *os.Root,r record,e SealedEvidence,l landingAttempt,result metadataResult)(heldComposition,error){
	var zero heldComposition
	raw,err:=boundedRegularBytes(p,"held-composition.json",maxHeldComposition)
	if err!=nil{return zero,err}
	var m heldComposition
	meta,err:=json.Marshal(result)
	if err!=nil||l.NewOnly||l.RestoreImageID!=0||len(e.Files)!=1||len(result.Rows)!=1||json.Unmarshal(raw,&m)!=nil||m.Version!=1||
		m.MetadataSHA256!=digestBytes(meta)||!validDigest(m.RetainedStateSHA256)||len(m.Retained)==0||len(m.Retained)>maxHeldAliases{return zero,ErrStageRecovery}
	f:=e.Files[0];h:=m.Held
	if h.PostID!=f.ID||h.ImageID!=result.Rows[0].ImageID||h.Size!=f.Size||h.SHA256!=f.SHA256||
		!validNamingRelative(h.CanonicalPath)||strings.EqualFold(h.CanonicalPath,finalName(r.Allocation,f))||h.CanonicalIdentity==""||len(h.CanonicalIdentity)>128{return zero,ErrStageRecovery}
	canon:=0
	seen:=map[string]bool{}
	for i,v:=range m.Retained{
		if v.ID<=0||(i>0&&v.ID<=m.Retained[i-1].ID)||!validNamingRelative(v.Relative)||strings.EqualFold(v.Relative,finalName(r.Allocation,f))||v.Identity==""||len(v.Identity)>128||seen[v.Identity]{return zero,ErrStageRecovery}
		seen[v.Identity]=true
		if v.Canonical{
			canon++
			if v.Relative!=h.CanonicalPath||v.Identity!=h.CanonicalIdentity{return zero,ErrStageRecovery}
		}
	}
	if canon!=1{return zero,ErrStageRecovery}
	rel,err:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil||m.RelationshipSHA256!=digestBytes(rel){return zero,ErrStageRecovery}
	collection,err:=collectionResultDigest(p)
	if err!=nil||m.CollectionSHA256!=collection{return zero,ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return m,nil
}

// Snapshot the actual retained state AFTER the verified source merge,
// relationships and collection application, excluding only the managed
// path row. No claims about pre-merge state. Bounded exact DB observation
// protects prior notes, ratings, attribution and other aliases through
// disposal; it does not repair or interpret later operator edits.
func retainedHeldState(ctx context.Context,g *library.Gallery,r record,dst *os.Root,f acquisitionplan.VerifiedTransferFile,h heldDuplicate)(string,[]retainedPath,error){
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return "",nil,ErrStageRecovery}
	defer tx.Rollback()
	alias:=filepath.Join(r.GalleryRoot,finalName(r.Allocation,f))
	var buf bytes.Buffer
	for _,q:=range []struct{query string;args []any}{
		{`SELECT * FROM images WHERE id=?`,[]any{h.ImageID}},
		{`SELECT * FROM image_sources WHERE image_id=? ORDER BY site COLLATE BINARY,post_id COLLATE BINARY LIMIT 8193`,[]any{h.ImageID}},
		{`SELECT * FROM image_annotations WHERE image_id=? ORDER BY id LIMIT 8193`,[]any{h.ImageID}},
		{`SELECT * FROM image_tags WHERE image_id=? ORDER BY tag_id LIMIT 8193`,[]any{h.ImageID}},
		{`SELECT * FROM image_tag_sources WHERE image_id=? ORDER BY tag_id,source COLLATE BINARY LIMIT 8193`,[]any{h.ImageID}},
		{`SELECT * FROM image_collections WHERE image_id=? ORDER BY name COLLATE BINARY LIMIT 8193`,[]any{h.ImageID}},
		{`SELECT * FROM derivative_edges WHERE source_image_id=? OR derivative_image_id=? ORDER BY source_image_id,derivative_image_id LIMIT 8193`,[]any{h.ImageID,h.ImageID}},
		{`SELECT * FROM image_paths WHERE image_id=? AND path<>? ORDER BY id LIMIT 65`,[]any{h.ImageID,alias}},
	}{
		rows,err:=tx.QueryContext(ctx,q.query,q.args...)
		if err!=nil{return "",nil,ErrStageRecovery}
		cols,err:=rows.Columns()
		if err!=nil{rows.Close();return "",nil,ErrStageRecovery}
		buf.WriteString(q.query);buf.WriteByte('\n')
		count:=0
		for rows.Next(){
			count++
			if count>8192||ctx.Err()!=nil{rows.Close();return "",nil,ErrStageLimit}
			vals:=make([]any,len(cols));ptrs:=make([]any,len(cols))
			for i:=range vals{ptrs[i]=&vals[i]}
			if err:=rows.Scan(ptrs...);err!=nil{rows.Close();return "",nil,ErrStageRecovery}
			raw,err:=json.Marshal(vals)
			if err!=nil||len(raw)>maxHeldStateBytes-buf.Len()-1{rows.Close();return "",nil,ErrStageLimit}
			buf.Write(raw);buf.WriteByte('\n')
		}
		err=rows.Err();rows.Close()
		if err!=nil{return "",nil,ErrStageRecovery}
	}
	rows,err:=tx.QueryContext(ctx,`SELECT id,path,is_canonical FROM image_paths WHERE image_id=? AND path<>? ORDER BY id LIMIT 65`,h.ImageID,alias)
	if err!=nil{return "",nil,ErrStageRecovery}
	paths:=[]retainedPath{}
	for rows.Next(){
		var v retainedPath;var path string;var canonical int
		if rows.Scan(&v.ID,&path,&canonical)!=nil||len(paths)>=maxHeldAliases||canonical<0||canonical>1||!filepath.IsAbs(path)||filepath.Clean(path)!=path{rows.Close();return "",nil,ErrStageRecovery}
		v.Relative,err=namingRelative(r.GalleryRoot,path)
		if err!=nil{rows.Close();return "",nil,err}
		v.Canonical=canonical==1
		paths=append(paths,v)
	}
	err=rows.Err();rows.Close()
	if err!=nil||len(paths)==0{return "",nil,ErrStageRecovery}
	canon:=0;seen:=map[string]bool{}
	for i,v:=range paths{
		id,err:=verifyHeldCanonical(ctx,dst,v.Relative,f)
		if err!=nil||seen[id]{return "",nil,ErrStageRecovery}
		seen[id]=true;paths[i].Identity=id
		if v.Canonical{canon++;if v.Relative!=h.CanonicalPath||id!=h.CanonicalIdentity{return "",nil,ErrStageRecovery}}
	}
	if canon!=1||tx.Commit()!=nil||ctx.Err()!=nil{return "",nil,ErrStageRecovery}
	return digestBytes(buf.Bytes()),paths,nil
}

func(a *Allocator)heldCompositionContext(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,extras extrasAuthority,h heldDuplicate,placed bool)(heldComposition,*os.Root,error){
	var zero heldComposition
	if g==nil||g.WatcherRunning()||extras==nil{return zero,nil,ErrStageRefused}
	job,err:=extras(ctx,grant)
	if err!=nil||!job.Empty(){return zero,nil,ErrStageRefused}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return zero,nil,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,nil,err}
	defer src.Close();defer dst.Close()
	fail:=func(err error)(heldComposition,*os.Root,error){p.Close();return zero,nil,err}
	if len(e.Files)!=1||len(snapshot.Rows)!=1||filepath.Ext(e.Files[0].RelativeName)!=".png"{return fail(ErrStageRefused)}
	l,err:=readLandingAttempt(p,r)
	if err!=nil||l.NewOnly||l.RestoreImageID!=0{return fail(ErrStageRefused)}
	current,err:=observedDB(r,g)
	if err!=nil||!sameLandingDB(current,l){return fail(ErrStageRecovery)}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return fail(err)}
	_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	if err!=nil||hash!=m.PolicySHA256{return fail(ErrStageRecovery)}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return fail(err)}
	rows,err:=selectMappedRows(snapshot,e)
	if err!=nil||len(rows)!=1||rows[0].Provenance==nil{return fail(ErrStageRefused)}
	row:=rows[0]
	v,err:=verifyMappedRow(ctx,g,m.Targets[0],row,result.Rows[0].Rejected)
	if err!=nil||!sameMetadataObservation(v,result.Rows[0])||h.ImageID!=v.ImageID{return fail(ErrStageRecovery)}
	if err:=validateRelationshipRecords(p,result,true);err!=nil{return fail(err)}
	relRaw,err:=boundedRegularBytes(p,"relationship-attempt.json",maxRelationshipRecord)
	var rel relationshipAttempt
	input,_,inputErr:=normalizedMappedSource(row)
	if err!=nil||inputErr!=nil||json.Unmarshal(relRaw,&rel)!=nil||rel.ImageID!=h.ImageID||rel.URL!=input.URL||rel.ParentURL!=input.ParentURL{return fail(ErrStageRecovery)}
	graph,err:=inspectRelationships(ctx,g,h.ImageID,rel.URL,rel.ParentURL)
	if err!=nil||!sameRelationships(expectedRelationships(rel.Before),graph){return fail(ErrStageRecovery)}
	if _,err:=collectionProof(ctx,g,p,l,result,row);err!=nil{return fail(err)}
	collection,err:=collectionResultDigest(p)
	if err!=nil{return fail(err)}
	relResult,err:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil{return fail(err)}
	meta,err:=json.Marshal(result)
	if err!=nil{return fail(ErrStageRecovery)}
	f:=e.Files[0]
	places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
	want:="absent";if placed{want="placed"}
	if err!=nil||len(places)!=1||places[0].State!=want{return fail(ErrStageRecovery)}
	var n,own int
	alias:=filepath.Join(r.GalleryRoot,finalName(grant,f))
	if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*),COALESCE(SUM(CASE WHEN image_id=? AND is_canonical=0 THEN 1 ELSE 0 END),0) FROM image_paths WHERE path=?`,h.ImageID,alias).Scan(&n,&own);err!=nil||n!=own||n>1||(placed&&n!=1){return fail(ErrStageRecovery)}
	var path string
	if err:=g.DB.Read.QueryRowContext(ctx,`SELECT canonical_path FROM images WHERE id=? AND sha256=? AND file_size=? AND is_missing=0`,h.ImageID,f.SHA256,f.Size).Scan(&path);err!=nil||path!=filepath.Join(r.GalleryRoot,h.CanonicalPath){return fail(ErrStageRecovery)}
	state,retained,err:=retainedHeldState(ctx,g,r,dst,f,h)
	if err!=nil{return fail(err)}
	s,err:=a.scope(ctx,grant.Request)
	current,dbErr:=observedDB(r,g)
	job,jobErr:=extras(ctx,grant)
	if err!=nil||!matches(r,s)||dbErr!=nil||!sameLandingDB(current,l)||jobErr!=nil||!job.Empty()||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return fail(ErrStageRefused)}
	return heldComposition{Version:1,Held:h,MetadataSHA256:digestBytes(meta),RelationshipSHA256:digestBytes(relResult),CollectionSHA256:collection,RetainedStateSHA256:state,Retained:retained},p,nil
}

func(a *Allocator)observeHeldCompositionLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,extras extrasAuthority,placed bool)(heldComposition,error){
	var zero heldComposition
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	l,err:=readLandingAttempt(p,r)
	if err!=nil{return zero,err}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return zero,err}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return zero,err}
	stored,err:=readHeldComposition(p,r,e,l,result)
	if err!=nil{return zero,err}
	actual,check,err:=a.heldCompositionContext(ctx,grant,g,extras,stored.Held,placed)
	if err!=nil{return zero,err}
	same:=sameRelationshipRoot(p,check);check.Close()
	if !same||!reflect.DeepEqual(stored,actual){return zero,ErrStageRecovery}
	return stored,nil
}
func(a *Allocator)disposeComposedHeld(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,extras extrasAuthority)(heldDuplicate,error){
	a.mu.Lock();defer a.mu.Unlock()
	var zero heldDuplicate
	held,err:=a.observeHeldDuplicatesLocked(ctx,grant,g)
	if err!=nil||len(held)!=1{return zero,ErrStageRecovery}
	m,p,err:=a.heldCompositionContext(ctx,grant,g,extras,held[0],true)
	if err!=nil{return zero,err}
	defer p.Close()
	for _,name:=range []string{"held-composition.json","duplicate-attempt.json","held-receipt.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	}
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxHeldComposition{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"held-composition.json",raw);err!=nil{return zero,err}
	syncDirectory(p)
	return a.disposeHeldDuplicateLocked(ctx,grant,g,extras)
}
func expectedHeldReceipt(p *os.Root,r record,e SealedEvidence,l landingAttempt,m metadataAttempt,result metadataResult)(heldReceipt,error){
	var zero heldReceipt
	composition,err:=readHeldComposition(p,r,e,l,result)
	if err!=nil{return zero,err}
	d,err:=readDuplicateAttempt(p,e,result)
	if err!=nil||d.Held!=composition.Held||d.CompositionSHA256==""{return zero,ErrStageRecovery}
	raw,err:=json.Marshal(d)
	if err!=nil{return zero,ErrStageRecovery}
	return heldReceipt{Version:1,Kind:heldReceiptKind,Allocation:r.Allocation,GalleryIdentity:r.GalleryIdentity,PolicySHA256:m.PolicySHA256,
		CompositionSHA256:d.CompositionSHA256,DisposalSHA256:digestBytes(raw),Held:d.Held},nil
}
func readHeldReceipt(p *os.Root,r record,e SealedEvidence,l landingAttempt,m metadataAttempt,result metadataResult)(heldReceipt,error){
	var zero heldReceipt
	want,err:=expectedHeldReceipt(p,r,e,l,m,result)
	if err!=nil{return zero,err}
	raw,err:=boundedRegularBytes(p,"held-receipt.json",maxHeldReceipt)
	canonical,marshalErr:=json.Marshal(want)
	if err!=nil||marshalErr!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return want,nil
}
func(a *Allocator)heldReceiptLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,extras extrasAuthority,write bool)(heldReceipt,error){
	var zero heldReceipt
	h,err:=a.observeDuplicateDisposalLocked(ctx,grant,g)
	if err!=nil{return zero,err}
	composition,err:=a.observeHeldCompositionLocked(ctx,grant,g,extras,false)
	if err!=nil||composition.Held!=h{return zero,ErrStageRecovery}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	l,err:=readLandingAttempt(p,r)
	if err!=nil{return zero,err}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return zero,err}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return zero,err}
	want,err:=expectedHeldReceipt(p,r,e,l,m,result)
	if err!=nil||want.Held!=h{return zero,ErrStageRecovery}
	job,err:=extras(ctx,grant)
	s,scopeErr:=a.scope(ctx,grant.Request)
	current,dbErr:=observedDB(r,g)
	if err!=nil||!job.Empty()||scopeErr!=nil||!matches(r,s)||dbErr!=nil||!sameLandingDB(current,l)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
	if !write{return readHeldReceipt(p,r,e,l,m,result)}
	if _,err:=p.Lstat("held-receipt.json");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	body,err:=json.Marshal(want)
	if err!=nil||len(body)>maxHeldReceipt{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"held-receipt.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	return a.heldReceiptLocked(ctx,grant,g,extras,false)
}
func(a *Allocator)finishHeldReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,extras extrasAuthority)(heldReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.heldReceiptLocked(ctx,grant,g,extras,true)
}
func(a *Allocator)observeHeldReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,extras extrasAuthority)(heldReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.heldReceiptLocked(ctx,grant,g,extras,false)
}
