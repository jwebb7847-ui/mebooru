package acquisitionstage

import(
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/library"
)

// Narrow guarded held disposition: only the operation-owned alias may
// be unlinked, with exact retained state and managed row identity bound
// to the completed metadata phase. The receipt is NOT yet accepted by
// common reclamation, and actual owner duplicate policy is not wired.
const existingHeldAttemptName="existing-held-attempt.json"
const existingHeldReceiptName="existing-held-receipt.json"
const existingHeldReceiptKind="guarded_existing_held_v1"
const maxExistingHeldRecordBytes=64<<10

type existingHeldAttempt struct{
	Version int
	MetadataSHA256 string
	ManagedPathID int64
	Held heldDuplicate
}
type existingHeldReceipt struct{
	Version int
	Witness terminalReceiptWitness
	AttemptSHA256 string
}

func expectedExistingHeldAttempt(plan existingTargetPlan,m existingMetadataAttempt,v existingMetadataResult)(existingHeldAttempt,error){
	var zero existingHeldAttempt
	if plan.Target.Kind!=existingHeld||plan.Target.Held==nil||m.Target.ImageID!=plan.Target.ImageID||v.Row.ImageID!=m.Target.ImageID||m.ManagedPathID<=0{return zero,ErrStageRefused}
	raw,err:=json.Marshal(v)
	if err!=nil||len(raw)>maxMetadataResultBytes{return zero,ErrStageRecovery}
	return existingHeldAttempt{Version:1,MetadataSHA256:digestBytes(raw),ManagedPathID:m.ManagedPathID,Held:*plan.Target.Held},nil
}
func readExistingHeldAttempt(p *os.Root,plan existingTargetPlan,m existingMetadataAttempt,v existingMetadataResult)(existingHeldAttempt,error){
	var zero existingHeldAttempt
	want,err:=expectedExistingHeldAttempt(plan,m,v)
	raw,readErr:=boundedRegularBytes(p,existingHeldAttemptName,maxExistingHeldRecordBytes)
	canonical,marshalErr:=json.Marshal(want)
	if err!=nil||readErr!=nil||marshalErr!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return want,nil
}
func expectedExistingHeldReceipt(r record,plan existingTargetPlan,m existingMetadataAttempt,v existingMetadataResult,h existingHeldAttempt)(existingHeldReceipt,error){
	var zero existingHeldReceipt
	want,err:=expectedExistingHeldAttempt(plan,m,v)
	if err!=nil||want!=h{return zero,ErrStageRecovery}
	raw,err:=json.Marshal(h)
	if err!=nil{return zero,ErrStageRecovery}
	var pathID int64
	for _,p:=range plan.Target.Retained{if p.Canonical{pathID=p.ID}}
	if pathID<=0{return zero,ErrStageRecovery}
	return existingHeldReceipt{Version:1,AttemptSHA256:digestBytes(raw),Witness:terminalReceiptWitness{
		Kind:existingHeldReceiptKind,Allocation:r.Allocation,GalleryIdentity:r.GalleryIdentity,PolicySHA256:m.PolicySHA256,
		Row:newReceiptRow{PostID:h.Held.PostID,ImageID:h.Held.ImageID,Name:h.Held.CanonicalPath,Size:h.Held.Size,SHA256:h.Held.SHA256},
		PathID:pathID,FileIdentity:h.Held.CanonicalIdentity,
	}},nil
}
func readExistingHeldReceipt(p *os.Root,r record,plan existingTargetPlan,m existingMetadataAttempt,v existingMetadataResult,h existingHeldAttempt)(existingHeldReceipt,error){
	var zero existingHeldReceipt
	want,err:=expectedExistingHeldReceipt(r,plan,m,v,h)
	raw,readErr:=boundedRegularBytes(p,existingHeldReceiptName,maxExistingHeldRecordBytes)
	canonical,marshalErr:=json.Marshal(want)
	if err!=nil||readErr!=nil||marshalErr!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return want,nil
}

type existingHeldEvidence struct{
	Plan existingTargetPlan
	Metadata existingMetadataAttempt
	Result existingMetadataResult
	Attempt existingHeldAttempt
	Receipt existingHeldReceipt
}

// Distinguish the three exact physical/DB states: placed alias; removed
// link with its exact row still present; completed removal of both.
// Never infer one from the other, or replay after a partial attempt.
func(a *Allocator) existingHeldEvidenceLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,phase string)(existingHeldEvidence,error){
	var zero existingHeldEvidence
	if g==nil||g.WatcherRunning()||(phase!="placed"&&phase!="unlinked"&&phase!="disposed"){return zero,ErrStageRefused}
	auth:=existingPlanAuthority(existingHeld,disposition,extras)
	if auth==nil{return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRecovery}
	plan,err:=readExistingTargetPlan(p,r,e.Files[0])
	if err!=nil||plan.Target.Kind!=existingHeld||plan.Target.Held==nil{return zero,ErrStageRefused}
	link,err:=readExistingLinkAttempt(p,r,plan)
	if err!=nil||readExistingLandingAttempt(p,link)!=nil{return zero,ErrStageRecovery}
	m,err:=readExistingMetadataAttempt(p,plan,link);if err!=nil{return zero,err}
	v,err:=readExistingMetadataResult(p,m);if err!=nil{return zero,err}
	h,err:=expectedExistingHeldAttempt(plan,m,v);if err!=nil{return zero,err}
	if phase!="placed"{
		stored,err:=readExistingHeldAttempt(p,plan,m,v);if err!=nil||stored!=h{return zero,ErrStageRecovery}
	}
	f:=e.Files[0]
	name:=finalName(grant,f)
	for i:=0;i<2;i++{
		job,err:=auth(ctx,grant);if err!=nil||!job.Empty(){return zero,ErrStageRefused}
		current,err:=observedDB(r,g);if err!=nil||current!=plan.Target.DB{return zero,ErrStageRecovery}
		snapshot,err:=a.boundPolicyLocked(ctx,grant);if err!=nil{return zero,err}
		_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
		rows,rowErr:=newOnlyRows(e,snapshot)
		if err!=nil||rowErr!=nil||len(rows)!=1||hash!=m.PolicySHA256||rows[0].SourceLabel!=m.Source||rows[0].PostURL!=m.URL{return zero,ErrStageRecovery}
		actual,err:=verifyExistingMappedRow(ctx,g,m,rows[0],v.Row.Rejected)
		if err!=nil||!sameMetadataObservation(actual,v.Row){return zero,ErrStageRecovery}
		graph,err:=inspectRelationships(ctx,g,h.Held.ImageID,m.URL,"")
		if err!=nil||graph.Parent!=0||len(graph.Children)!=0||len(graph.Edges)!=0{return zero,ErrStageRecovery}
		places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
		want,links:="absent",uint64(1)
		if phase=="placed"{want,links="placed",2}
		if err!=nil||len(places)!=1||places[0].State!=want{return zero,ErrStageRecovery}
		identity,err:=namingFile(ctx,src,f.RelativeName,f,links)
		if err!=nil||identity!=plan.Target.FileIdentity||verifyPlannedImageRow(ctx,g,h.Held.ImageID,f,&plan.Target.Media)!=nil{return zero,ErrStageRecovery}
		var count int
		if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM images WHERE id=? AND canonical_path=? AND sha256=? AND is_missing=0`,h.Held.ImageID,filepath.Join(r.GalleryRoot,h.Held.CanonicalPath),f.SHA256).Scan(&count);err!=nil||count!=1{return zero,ErrStageRecovery}
		path:=filepath.Join(r.GalleryRoot,name)
		if phase=="disposed"{
			if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_paths WHERE path=?`,path).Scan(&count);err!=nil||count!=0{return zero,ErrStageRecovery}
		}else{
			if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_paths WHERE id=? AND image_id=? AND path=? AND is_canonical=0`,h.ManagedPathID,h.Held.ImageID,path).Scan(&count);err!=nil||count!=1{return zero,ErrStageRecovery}
			preserved,err:=existingPreservedState(ctx,g,h.Held.ImageID,m.Source)
			if err!=nil||preserved!=m.PreservedSHA256{return zero,ErrStageRecovery}
		}
		state,retained,err:=retainedHeldState(ctx,g,r,dst,f,h.Held)
		if err!=nil||state!=v.StateSHA256||!reflect.DeepEqual(retained,plan.Target.Retained){return zero,ErrStageRecovery}
		s,err:=a.scope(ctx,grant.Request)
		if err!=nil||!matches(r,s)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
	}
	again,err:=readExistingTargetPlan(p,r,f);if err!=nil||!sameExistingPublicationTarget(again.Target,plan.Target){return zero,ErrStageRecovery}
	next,err:=readExistingLinkAttempt(p,r,again);if err!=nil||!reflect.DeepEqual(link,next)||readExistingLandingAttempt(p,next)!=nil{return zero,ErrStageRecovery}
	meta,err:=readExistingMetadataAttempt(p,again,next);if err!=nil||!reflect.DeepEqual(meta,m){return zero,ErrStageRecovery}
	result,err:=readExistingMetadataResult(p,meta);if err!=nil||!reflect.DeepEqual(result,v){return zero,ErrStageRecovery}
	if phase!="placed"{stored,err:=readExistingHeldAttempt(p,again,meta,result);if err!=nil||stored!=h{return zero,ErrStageRecovery}}
	receipt,err:=expectedExistingHeldReceipt(r,plan,m,v,h);if err!=nil{return zero,err}
	return existingHeldEvidence{Plan:plan,Metadata:m,Result:v,Attempt:h,Receipt:receipt},nil
}

func(a *Allocator) disposeExistingHeld(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	before,err:=a.existingHeldEvidenceLocked(ctx,grant,g,disposition,extras,"placed")
	if err!=nil{return err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	for _,name:=range []string{existingHeldAttemptName,existingHeldReceiptName}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	raw,err:=json.Marshal(before.Attempt)
	if err!=nil||len(raw)>maxExistingHeldRecordBytes{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,existingHeldAttemptName,raw);err!=nil{return err}
	syncDirectory(p)
	current,err:=a.existingHeldEvidenceLocked(ctx,grant,g,disposition,extras,"placed")
	if err!=nil||!reflect.DeepEqual(before,current)||!a.livePublicationRoots(grant,r,p,src,dst){return ErrStageRecovery}
	h,err:=readExistingHeldAttempt(p,before.Plan,before.Metadata,before.Result)
	if err!=nil||h!=before.Attempt{return ErrStageRecovery}
	f:=e.Files[0];name:=finalName(grant,f)
	if err:=dst.Remove(name);err!=nil{return ErrStageRecovery}
	syncDirectory(dst)
	current,err=a.existingHeldEvidenceLocked(ctx,grant,g,disposition,extras,"unlinked")
	if err!=nil||!reflect.DeepEqual(before,current)||!a.livePublicationRoots(grant,r,p,src,dst){return ErrStageRecovery}
	err=g.Edit(func(database *db.DB)error{
		res,err:=database.Write.ExecContext(ctx,`DELETE FROM image_paths WHERE id=? AND image_id=? AND path=? AND is_canonical=0 AND EXISTS (SELECT 1 FROM images WHERE id=? AND canonical_path=? AND sha256=? AND is_missing=0)`,
			h.ManagedPathID,h.Held.ImageID,filepath.Join(r.GalleryRoot,name),h.Held.ImageID,filepath.Join(r.GalleryRoot,h.Held.CanonicalPath),f.SHA256)
		if err!=nil{return err}
		n,err:=res.RowsAffected();if err!=nil||n!=1{return ErrStageRecovery}
		return nil
	})
	if err!=nil{return ErrStageRecovery}
	current,err=a.existingHeldEvidenceLocked(ctx,grant,g,disposition,extras,"disposed")
	if err!=nil||!reflect.DeepEqual(before,current){return ErrStageRecovery}
	return nil
}

func(a *Allocator) finishExistingHeldReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority)(existingHeldReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.existingHeldReceiptLocked(ctx,grant,g,disposition,extras,true)
}
func(a *Allocator) observeExistingHeldReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority)(existingHeldReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.existingHeldReceiptLocked(ctx,grant,g,disposition,extras,false)
}
func(a *Allocator) existingHeldReceiptLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,write bool)(existingHeldReceipt,error){
	var zero existingHeldReceipt
	before,err:=a.existingHeldEvidenceLocked(ctx,grant,g,disposition,extras,"disposed")
	if err!=nil{return zero,err}
	r,_,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if !write{return readExistingHeldReceipt(p,r,before.Plan,before.Metadata,before.Result,before.Attempt)}
	if _,err:=p.Lstat(existingHeldReceiptName);!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	raw,err:=json.Marshal(before.Receipt)
	if err!=nil||len(raw)>maxExistingHeldRecordBytes{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,existingHeldReceiptName,raw);err!=nil{return zero,err}
	syncDirectory(p)
	return a.existingHeldReceiptLocked(ctx,grant,g,disposition,extras,false)
}
