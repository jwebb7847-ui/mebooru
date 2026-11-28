package acquisitionstage

import(
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
	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// Isolated guarded restoration. Reuse the qualified rooted naming
// primitives, but never synthesize a legacy landing or naming marker.
// Full post-merge state is normalized only for the intentional canonical
// path move. No automatic compensation/retry; retain the private copies.
const existingRestoreAttemptName="existing-restore-attempt.json"
const existingRestoreReceiptName="existing-restore-receipt.json"
const existingRestoreReceiptKind="guarded_existing_restored_v1"
const maxExistingRestoreRecordBytes=128<<10

type existingRestoreAttempt struct{
	Version int
	MetadataSHA256 string
	Move namingAttempt
}
type existingRestoreReceipt struct{
	Version int
	Witness terminalReceiptWitness
	AttemptSHA256 string
}

func expectedExistingRestoreAttempt(r record,plan existingTargetPlan,link existingLinkAttempt,m existingMetadataAttempt,v existingMetadataResult)(existingRestoreAttempt,error){
	var zero existingRestoreAttempt
	if plan.Target.Kind!=existingRestore||plan.Target.Restore==nil||link.Naming==nil||link.Preview==nil||
		m.Target.ImageID!=plan.Target.ImageID||v.Row.ImageID!=m.Target.ImageID||m.ManagedPathID<=0||!validDigest(v.StateSHA256){return zero,ErrStageRefused}
	initial,err:=namingRelative(r.GalleryRoot,link.Preview.InitialPath)
	final,other:=namingRelative(r.GalleryRoot,link.Preview.FinalPath)
	old:=finalName(r.Allocation,plan.Target.File)
	if err!=nil||other!=nil||strings.EqualFold(old,final)||strings.EqualFold(old,initial)||
		strings.EqualFold(initial,plan.Target.Restore.RestorePath)||strings.EqualFold(final,plan.Target.Restore.RestorePath){return zero,ErrStageRecovery}
	raw,err:=json.Marshal(v)
	if err!=nil||len(raw)>maxMetadataResultBytes{return zero,ErrStageRecovery}
	return existingRestoreAttempt{Version:1,MetadataSHA256:digestBytes(raw),Move:namingAttempt{
		Version:1,MetadataSHA256:digestBytes(raw),RestoreStateSHA256:v.StateSHA256,Request:*link.Naming,
		ImageID:m.Target.ImageID,PathID:m.ManagedPathID,Old:old,Initial:initial,Final:final,FileIdentity:plan.Target.FileIdentity,
		InitialSuffixed:link.Preview.InitialSuffixed,FinalSuffixed:link.Preview.FinalSuffixed,
	}},nil
}
func readExistingRestoreAttempt(p *os.Root,r record,plan existingTargetPlan,link existingLinkAttempt,m existingMetadataAttempt,v existingMetadataResult)(existingRestoreAttempt,error){
	var zero existingRestoreAttempt
	want,err:=expectedExistingRestoreAttempt(r,plan,link,m,v)
	raw,readErr:=boundedRegularBytes(p,existingRestoreAttemptName,maxExistingRestoreRecordBytes)
	canonical,marshalErr:=json.Marshal(want)
	if err!=nil||readErr!=nil||marshalErr!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return want,nil
}
func expectedExistingRestoreReceipt(r record,plan existingTargetPlan,m existingMetadataAttempt,n existingRestoreAttempt)(existingRestoreReceipt,error){
	var zero existingRestoreReceipt
	if plan.Target.Kind!=existingRestore||plan.Target.Restore==nil||n.Move.ImageID!=plan.Target.ImageID||
		n.Move.PathID!=m.ManagedPathID||n.Move.FileIdentity!=plan.Target.FileIdentity{return zero,ErrStageRecovery}
	raw,err:=json.Marshal(n)
	if err!=nil{return zero,ErrStageRecovery}
	f:=plan.Target.File
	return existingRestoreReceipt{Version:1,AttemptSHA256:digestBytes(raw),Witness:terminalReceiptWitness{
		Kind:existingRestoreReceiptKind,Allocation:r.Allocation,GalleryIdentity:r.GalleryIdentity,PolicySHA256:m.PolicySHA256,
		Row:newReceiptRow{PostID:f.ID,ImageID:n.Move.ImageID,Name:n.Move.Final,Size:f.Size,SHA256:f.SHA256},
		PathID:n.Move.PathID,FileIdentity:n.Move.FileIdentity,
	}},nil
}
func readExistingRestoreReceipt(p *os.Root,r record,plan existingTargetPlan,m existingMetadataAttempt,n existingRestoreAttempt)(existingRestoreReceipt,error){
	var zero existingRestoreReceipt
	want,err:=expectedExistingRestoreReceipt(r,plan,m,n)
	raw,readErr:=boundedRegularBytes(p,existingRestoreReceiptName,maxExistingRestoreRecordBytes)
	canonical,marshalErr:=json.Marshal(want)
	if err!=nil||readErr!=nil||marshalErr!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return want,nil
}

type existingRestoreEvidence struct{
	Plan existingTargetPlan
	Link existingLinkAttempt
	Metadata existingMetadataAttempt
	Result existingMetadataResult
	Attempt existingRestoreAttempt
	Receipt existingRestoreReceipt
}

// Four exact observations: original managed canonical, new link with
// old DB row, repointed DB with both links, final canonical only.
func(a *Allocator) existingRestoreEvidenceLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority,phase string)(existingRestoreEvidence,error){
	var zero existingRestoreEvidence
	if g==nil||g.WatcherRunning()||naming==nil||(phase!="placed"&&phase!="linked"&&phase!="repointed"&&phase!="named"){return zero,ErrStageRefused}
	auth:=existingPlanAuthority(existingRestore,disposition,extras)
	if auth==nil{return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRecovery}
	plan,err:=readExistingTargetPlan(p,r,e.Files[0])
	if err!=nil||plan.Target.Kind!=existingRestore||plan.Target.Restore==nil{return zero,ErrStageRefused}
	link,err:=readExistingLinkAttempt(p,r,plan)
	if err!=nil||readExistingLandingAttempt(p,link)!=nil{return zero,ErrStageRecovery}
	m,err:=readExistingMetadataAttempt(p,plan,link);if err!=nil{return zero,err}
	v,err:=readExistingMetadataResult(p,m);if err!=nil{return zero,err}
	n,err:=expectedExistingRestoreAttempt(r,plan,link,m,v);if err!=nil{return zero,err}
	if phase!="placed"{stored,err:=readExistingRestoreAttempt(p,r,plan,link,m,v);if err!=nil||stored!=n{return zero,ErrStageRecovery}}
	move:=n.Move
	f:=e.Files[0]
	l:=*plan.Target.Restore
	parent,_,err:=namingParent(dst,move.Final)
	if err!=nil{return zero,err}
	defer parent.Close()
	for i:=0;i<2;i++{
		job,err:=auth(ctx,grant);if err!=nil||!job.Empty(){return zero,ErrStageRefused}
		q,err:=naming(ctx,grant);if err!=nil||q!=move.Request{return zero,ErrStageRefused}
		current,err:=observedDB(r,g);if err!=nil||current!=plan.Target.DB{return zero,ErrStageRecovery}
		snapshot,err:=a.boundPolicyLocked(ctx,grant);if err!=nil{return zero,err}
		_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
		rows,rowErr:=newOnlyRows(e,snapshot)
		if err!=nil||rowErr!=nil||len(rows)!=1||hash!=m.PolicySHA256||rows[0].SourceLabel!=m.Source||rows[0].PostURL!=m.URL{return zero,ErrStageRecovery}
		actual,err:=verifyExistingMappedRow(ctx,g,m,rows[0],v.Row.Rejected)
		if err!=nil||!sameMetadataObservation(actual,v.Row){return zero,ErrStageRecovery}
		graph,err:=inspectRelationships(ctx,g,move.ImageID,m.URL,"")
		if err!=nil||graph.Parent!=0||len(graph.Children)!=0||len(graph.Edges)!=0{return zero,ErrStageRecovery}
		if _,err:=dst.Lstat(l.RestorePath);!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
		if verifyPlannedImageRow(ctx,g,move.ImageID,f,&plan.Target.Media)!=nil{return zero,ErrStageRecovery}
		state,err:=restoreStateDigest(ctx,g,move.ImageID)
		if err!=nil||state!=v.StateSHA256{return zero,ErrStageRecovery}
		old,new:=phase!="named",phase!="placed"
		rel,absent:=move.Old,move.Final
		if phase=="repointed"||phase=="named"{rel,absent=move.Final,move.Old}
		pid,err:=restoredNamingRows(ctx,g,r,f,move.ImageID,rel,absent,l)
		if err!=nil||pid!=move.PathID{return zero,ErrStageRecovery}
		if err:=checkNamingFiles(ctx,src,dst,parent,f,move,old,new);err!=nil{return zero,err}
		check:=newPublicationPlan{File:f,Preview:*link.Preview,DestinationIdentity:link.DestinationIdentity}
		if err:=verifyPlanDestinations(r,dst,&check);err!=nil{return zero,err}
		if phase=="placed"{
			preserved,err:=existingPreservedState(ctx,g,move.ImageID,m.Source)
			if err!=nil||preserved!=m.PreservedSHA256{return zero,ErrStageRecovery}
			preview,err:=gallery.PreviewReceivedNaming(ctx,g.DB,g.Boundary(),move.ImageID,q)
			if err!=nil||preview!=*link.Preview{return zero,ErrStageRecovery}
		}
		s,err:=a.scope(ctx,grant.Request)
		if err!=nil||!matches(r,s)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst)||!namingParentLive(r,dst,parent,move.Final)||
			g.Boundary().Check(filepath.Join(r.GalleryRoot,move.Final))!=nil||g.Boundary().Check(filepath.Join(r.GalleryRoot,move.Old))!=nil{return zero,ErrStageRefused}
	}
	again,err:=readExistingTargetPlan(p,r,f);if err!=nil||!sameExistingPublicationTarget(again.Target,plan.Target){return zero,ErrStageRecovery}
	next,err:=readExistingLinkAttempt(p,r,again);if err!=nil||!reflect.DeepEqual(next,link)||readExistingLandingAttempt(p,next)!=nil{return zero,ErrStageRecovery}
	meta,err:=readExistingMetadataAttempt(p,again,next);if err!=nil||!reflect.DeepEqual(meta,m){return zero,ErrStageRecovery}
	result,err:=readExistingMetadataResult(p,meta);if err!=nil||!reflect.DeepEqual(result,v){return zero,ErrStageRecovery}
	if phase!="placed"{stored,err:=readExistingRestoreAttempt(p,r,again,next,meta,result);if err!=nil||stored!=n{return zero,ErrStageRecovery}}
	receipt,err:=expectedExistingRestoreReceipt(r,plan,m,n);if err!=nil{return zero,err}
	return existingRestoreEvidence{Plan:plan,Link:link,Metadata:m,Result:v,Attempt:n,Receipt:receipt},nil
}

func(a *Allocator) nameExistingRestore(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	before,err:=a.existingRestoreEvidenceLocked(ctx,grant,g,disposition,extras,naming,"placed")
	if err!=nil{return err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	for _,name:=range []string{existingRestoreAttemptName,existingRestoreReceiptName}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	n:=before.Attempt
	move:=n.Move
	parent,_,err:=namingParent(dst,move.Final)
	if err!=nil{return err}
	defer parent.Close()
	raw,err:=json.Marshal(n)
	if err!=nil||len(raw)>maxExistingRestoreRecordBytes{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,existingRestoreAttemptName,raw);err!=nil{return err}
	syncDirectory(p)
	guard:=func(phase string)error{
		current,err:=a.existingRestoreEvidenceLocked(ctx,grant,g,disposition,extras,naming,phase)
		if err!=nil||!reflect.DeepEqual(before,current)||!a.livePublicationRoots(grant,r,p,src,dst)||!namingParentLive(r,dst,parent,move.Final){return ErrStageRecovery}
		stored,err:=readExistingRestoreAttempt(p,r,before.Plan,before.Link,before.Metadata,before.Result)
		if err!=nil||stored!=n{return ErrStageRecovery}
		return nil
	}
	if err:=guard("placed");err!=nil{return err}
	if err:=dst.Link(move.Old,move.Final);err!=nil{return ErrStageRecovery}
	syncDirectory(parent)
	if err:=guard("linked");err!=nil{return err}
	f:=e.Files[0]
	oldPath,newPath:=filepath.Join(r.GalleryRoot,move.Old),filepath.Join(r.GalleryRoot,move.Final)
	err=g.Edit(func(d *db.DB)error{
		return db.InWriteTx(d.Write,func(tx *sql.Tx)error{
			res,err:=tx.ExecContext(ctx,`UPDATE images SET canonical_path=?,folder_path=? WHERE id=? AND canonical_path=? AND sha256=? AND file_size=? AND is_missing=0`,
				newPath,gallery.FolderPath(r.GalleryRoot,newPath),move.ImageID,oldPath,f.SHA256,f.Size)
			if err!=nil{return err}
			count,err:=res.RowsAffected();if err!=nil||count!=1{return ErrStageRecovery}
			res,err=tx.ExecContext(ctx,`UPDATE image_paths SET path=? WHERE id=? AND image_id=? AND path=? AND is_canonical=1 AND NOT EXISTS(SELECT 1 FROM image_paths WHERE path=?)`,
				newPath,move.PathID,move.ImageID,oldPath,newPath)
			if err!=nil{return err}
			count,err=res.RowsAffected();if err!=nil||count!=1{return ErrStageRecovery}
			return nil
		})
	})
	if err!=nil{return ErrStageRecovery}
	if err:=guard("repointed");err!=nil{return err}
	if err:=dst.Remove(move.Old);err!=nil{return ErrStageRecovery}
	syncDirectory(dst)
	return guard("named")
}
func(a *Allocator) finishExistingRestoreReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)(existingRestoreReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.existingRestoreReceiptLocked(ctx,grant,g,disposition,extras,naming,true)
}
func(a *Allocator) observeExistingRestoreReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)(existingRestoreReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.existingRestoreReceiptLocked(ctx,grant,g,disposition,extras,naming,false)
}
func(a *Allocator) existingRestoreReceiptLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority,write bool)(existingRestoreReceipt,error){
	var zero existingRestoreReceipt
	before,err:=a.existingRestoreEvidenceLocked(ctx,grant,g,disposition,extras,naming,"named")
	if err!=nil{return zero,err}
	r,_,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if !write{return readExistingRestoreReceipt(p,r,before.Plan,before.Metadata,before.Attempt)}
	if _,err:=p.Lstat(existingRestoreReceiptName);!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	raw,err:=json.Marshal(before.Receipt)
	if err!=nil||len(raw)>maxExistingRestoreRecordBytes{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,existingRestoreReceiptName,raw);err!=nil{return zero,err}
	syncDirectory(p)
	return a.existingRestoreReceiptLocked(ctx,grant,g,disposition,extras,naming,false)
}
