package acquisitionstage

import(
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// A batch uses a single approved, verified transfer and deterministic
// child operation IDs. Child workspaces are LOCAL copies, never new WAN
// grants. The owner must explicitly authorize each narrowed request.
// A stopped batch preserves its parent and all completed child receipts.
const maxBatchRecord=8<<20
type batchItem struct{
	PostID string
	Request workerproto.StageAllocateRequest
	File acquisitionplan.VerifiedTransferFile
}
type batchPlan struct{
	Version int
	Parent workerproto.StageAllocation
	PolicySHA256 string
	Items []batchItem
}
type BatchItemOutcome struct{
	PostID string
	State string
	Receipt *OwnerReceipt
}
type BatchOutcome struct{
	Version int
	PlanSHA256 string
	PolicySHA256 string
	Items []BatchItemOutcome
}
type BatchOwner struct{
	Current func(context.Context,workerproto.StageAllocation)error
	// Archive is optional; it moves already-closed child evidence intact
	// out of active inventory as a normal batch progresses.
	Archive HistoryAuthority
	// Child must independently authorize the exact narrowed request and
	// return current owner policy/custody callbacks for it.
	Child func(context.Context,workerproto.StageAllocation,workerproto.StageAllocateRequest,acquisitionplan.ApprovedPolicyRow)(OwnerCommit,error)
}

func makeBatchPlan(r record,e SealedEvidence,manifest []byte,snapshot acquisitionplan.ApprovedPolicySnapshot)(batchPlan,error){
	var zero batchPlan
	if snapshot.Transfer!=r.Allocation.Request.Transfer{return zero,ErrStageRecovery}
	_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	if err!=nil{return zero,err}
	var native map[string]json.RawMessage
	if json.Unmarshal(manifest,&native)!=nil{return zero,ErrStageRecovery}
	files:=map[string]acquisitionplan.VerifiedTransferFile{}
	for _,f:=range e.Files{files[f.ID]=f}
	p:=batchPlan{Version:1,Parent:r.Allocation,PolicySHA256:hash,Items:make([]batchItem,0,len(snapshot.Rows))}
	for _,row:=range snapshot.Rows{
		item:=batchItem{PostID:row.PostID}
		if f,ok:=files[row.PostID];ok{
			one,err:=json.Marshal(map[string]json.RawMessage{row.PostID:native[row.PostID]})
			if err!=nil||len(native[row.PostID])==0{return zero,ErrStageRecovery}
			q:=r.Allocation.Request
			q.OperationID=digestBytes([]byte("mebooru-batch-child-v1:"+q.OperationID+":"+row.PostID))[:32]
			q.Transfer.ManifestSHA256=digestBytes(one)
			if !workerproto.ValidStageAllocate(q)||q.OperationID==r.Allocation.Request.OperationID{return zero,ErrStageRecovery}
			item.File=f;item.Request=q
		}
		p.Items=append(p.Items,item)
	}
	return p,nil
}
func readBatchInputs(op *os.Root,r record,e SealedEvidence)([]byte,acquisitionplan.ApprovedPolicySnapshot,error){
	var zero acquisitionplan.ApprovedPolicySnapshot
	sealed,err:=openDir(op,"sealed");if err!=nil{return nil,zero,err};defer sealed.Close()
	manifest,err:=boundedRegularBytes(sealed,"manifest.json",workerproto.MaxMetadataOutputBytes);if err!=nil{return nil,zero,err}
	snapshot,err:=verifyBoundPolicy(sealed,e,manifest)
	return manifest,snapshot,err
}
func readBatchPlan(op *os.Root,r record,e SealedEvidence)(batchPlan,error){
	var zero batchPlan
	if _,err:=op.Lstat("publication");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	b,err:=openDir(op,"batch");if err!=nil{return zero,err};defer b.Close()
	entries,err:=list(b,len(e.Files)+4);if err!=nil{return zero,err}
	manifest,snapshot,err:=readBatchInputs(op,r,e);if err!=nil{return zero,err}
	want,err:=makeBatchPlan(r,e,manifest,snapshot);if err!=nil{return zero,err}
	raw,err:=boundedRegularBytes(b,"plan.json",maxBatchRecord)
	body,e2:=json.Marshal(want)
	if err!=nil||e2!=nil||!bytes.Equal(raw,body){return zero,ErrStageRecovery}
	allowed:=map[string]bool{"plan.json":true,"result.json":true,"reclaim-attempt.json":true,"reclaim-result.json":true}
	for _,i:=range want.Items{if i.File.ID!=""{allowed[i.Request.OperationID+".json"]=true}}
	for _,x:=range entries{if !allowed[x.Name()]||!x.Type().IsRegular(){return zero,ErrStageRecovery}}
	return want,nil
}
func readBatchItem(b *os.Root,item batchItem)(BatchItemOutcome,error){
	var zero BatchItemOutcome
	raw,err:=boundedRegularBytes(b,item.Request.OperationID+".json",maxRecordBytes)
	var out BatchItemOutcome
	if err!=nil||json.Unmarshal(raw,&out)!=nil||out.PostID!=item.PostID||out.State!="committed"||out.Receipt==nil||
		out.Receipt.Allocation.Request!=item.Request||out.Receipt.Row.PostID!=item.PostID||out.Receipt.Row.SHA256!=item.File.SHA256||
		out.Receipt.Row.Size!=item.File.Size||!validDigest(out.Receipt.ReceiptSHA256){return zero,ErrStageRecovery}
	canonical,err:=json.Marshal(out);if err!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return out,nil
}
func batchOutcome(b *os.Root,p batchPlan,partial bool)(BatchOutcome,error){
	var zero BatchOutcome
	raw,err:=json.Marshal(p);if err!=nil{return zero,err}
	out:=BatchOutcome{Version:1,PlanSHA256:digestBytes(raw),PolicySHA256:p.PolicySHA256,Items:make([]BatchItemOutcome,0,len(p.Items))}
	for _,i:=range p.Items{
		if i.File.ID==""{out.Items=append(out.Items,BatchItemOutcome{PostID:i.PostID,State:"not_transferred"});continue}
		if _,err:=b.Lstat(i.Request.OperationID+".json");errors.Is(err,os.ErrNotExist)&&partial{
			out.Items=append(out.Items,BatchItemOutcome{PostID:i.PostID,State:"unresolved"});continue
		}
		v,err:=readBatchItem(b,i);if err!=nil{return zero,err}
		out.Items=append(out.Items,v)
	}
	return out,nil
}
func readBatchResult(b *os.Root,p batchPlan)(BatchOutcome,error){
	var zero BatchOutcome
	want,err:=batchOutcome(b,p,false);if err!=nil{return zero,err}
	raw,err:=boundedRegularBytes(b,"result.json",maxBatchRecord)
	body,e2:=json.Marshal(want)
	if err!=nil||e2!=nil||!bytes.Equal(raw,body){return zero,ErrStageRecovery}
	return want,nil
}

func(a *Allocator) beginBatch(ctx context.Context,parent workerproto.StageAllocation,owner BatchOwner)(batchPlan,[]byte,acquisitionplan.ApprovedPolicySnapshot,error){
	a.mu.Lock();defer a.mu.Unlock()
	var zero batchPlan
	var empty acquisitionplan.ApprovedPolicySnapshot
	if owner.Current==nil||owner.Child==nil||owner.Current(ctx,parent)!=nil{return zero,nil,empty,ErrStageRefused}
	e,err:=a.sealedLocked(ctx,parent);if err!=nil{return zero,nil,empty,err}
	r,err:=a.load(parent.Request.OperationID);if err!=nil||r.Allocation!=parent{return zero,nil,empty,ErrStageRefused}
	op,err:=openDir(a.root,parent.Request.OperationID);if err!=nil{return zero,nil,empty,err};defer op.Close()
	for _,n:=range []string{"publication","batch"}{if _,err:=op.Lstat(n);!errors.Is(err,os.ErrNotExist){return zero,nil,empty,ErrStageRefused}}
	manifest,snapshot,err:=readBatchInputs(op,r,e);if err!=nil{return zero,nil,empty,err}
	p,err:=makeBatchPlan(r,e,manifest,snapshot);if err!=nil{return zero,nil,empty,err}
	raw,err:=json.Marshal(p);if err!=nil||len(raw)>maxBatchRecord{return zero,nil,empty,ErrStageLimit}
	if owner.Current(ctx,parent)!=nil||ctx.Err()!=nil||!a.live(){return zero,nil,empty,ErrStageRefused}
	if err:=op.Mkdir("batch",0700);err!=nil{return zero,nil,empty,ErrStageRecovery}
	syncDirectory(op)
	b,err:=openDir(op,"batch");if err!=nil{return zero,nil,empty,err};defer b.Close()
	if err:=writeOwnerBytes(ctx,b,"plan.json",raw);err!=nil{return zero,nil,empty,err};syncDirectory(b)
	return p,manifest,snapshot,nil
}

func(a *Allocator) copyBatchChild(ctx context.Context,parent workerproto.StageAllocation,item batchItem,manifest []byte,row acquisitionplan.ApprovedPolicyRow,auth OwnerCommit)(workerproto.StageAllocation,error){
	var zero workerproto.StageAllocation
	if auth.Current==nil{return zero,ErrStageRefused}
	// Never reuse a partial child workspace. The batch itself is one-shot;
	// allocation replay is not permission to copy/commit again.
	child,err:=a.Allocate(ctx,item.Request);if err!=nil{return zero,err}
	if child.MaxFiles<1||uint64(item.File.Size)+uint64(item.File.SidecarSize)>child.MaxBytes||auth.Current(ctx,child)!=nil{return zero,ErrStageRefused}
	a.mu.Lock()
	err=func()error{
		if ctx.Err()!=nil||!a.live(){return ErrStageRefused}
		op,err:=openDir(a.root,parent.Request.OperationID);if err!=nil{return err};defer op.Close()
		sealed,err:=openDir(op,"sealed");if err!=nil{return err};defer sealed.Close()
		src,err:=openDir(sealed,"media");if err!=nil{return err};defer src.Close()
		co,err:=openDir(a.root,child.Request.OperationID);if err!=nil{return err};defer co.Close()
		cr,err:=readRecord(co);if err!=nil||cr.Allocation!=child{return ErrStageRecovery}
		pr,err:=readRecord(op);if err!=nil||pr.Allocation!=parent||cr.GalleryIdentity!=pr.GalleryIdentity||cr.GalleryRoot!=pr.GalleryRoot{return ErrStageRefused}
		w,err:=openDir(co,child.TransferID);if err!=nil{return err};defer w.Close()
		if entries,err:=list(w,0);err!=nil||len(entries)!=0{return ErrStageRecovery}
		if err:=writeOwnerBytes(ctx,w,"strict-blacklist.json",manifest);err!=nil{return err}
		if err:=writeOwnerBytes(ctx,w,"strict-blacklist-probe.json",[]byte(`{"1":{"category":"directlink","tags":["probe"]}}`));err!=nil{return err}
		if err:=w.Mkdir("media",0700);err!=nil{return ErrStageRecovery}
		dst,err:=openDir(w,"media");if err!=nil{return err};defer dst.Close()
		f:=item.File
		if err:=writeChecked(ctx,dst,f.RelativeName,f.Size,f.SHA256,func(out io.Writer)error{return readChecked(ctx,src,f.RelativeName,f.Size,f.SHA256,out)});err!=nil{return err}
		if err:=writeChecked(ctx,dst,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256,func(out io.Writer)error{return readChecked(ctx,src,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256,out)});err!=nil{return err}
		syncDirectory(dst);syncDirectory(w)
		return nil
	}()
	a.mu.Unlock()
	if err!=nil{return zero,err}
	if _,err:=a.Seal(ctx,child,func(context.Context,workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){
		if auth.Current(ctx,child)!=nil{return nil,nil,ErrStageRefused}
		return manifest,[]acquisitionplan.VerifiedTransferFile{item.File},nil
	});err!=nil{return zero,err}
	body,hash,err:=acquisitionplan.EncodeApprovedPolicy(child.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row});if err!=nil{return zero,err}
	if err:=a.BindPolicy(ctx,child,func(context.Context,workerproto.StageAllocation)([]byte,string,error){
		if auth.Current(ctx,child)!=nil{return nil,"",ErrStageRefused};return body,hash,nil
	});err!=nil{return zero,err}
	return child,nil
}

func(a *Allocator) persistBatchItem(ctx context.Context,parent workerproto.StageAllocation,p batchPlan,item batchItem,w OwnerReceipt)error{
	a.mu.Lock();defer a.mu.Unlock()
	r,err:=a.load(parent.Request.OperationID);if err!=nil{return err}
	op,err:=openDir(a.root,parent.Request.OperationID);if err!=nil{return err};defer op.Close()
	e,err:=readSeal(op,parent);if err!=nil{return err}
	current,err:=readBatchPlan(op,r,e);if err!=nil||!reflect.DeepEqual(p,current){return ErrStageRecovery}
	cr,err:=a.load(item.Request.OperationID);if err!=nil||cr.Allocation!=w.Allocation{return ErrStageRecovery}
	done,_,err:=a.closedCharge(ctx,cr);if err!=nil||!done{return ErrStageRecovery}
	co,err:=openDir(a.root,item.Request.OperationID);if err!=nil{return err};defer co.Close()
	ce,err:=readSeal(co,cr.Allocation);if err!=nil{return err}
	cp,err:=openDir(co,"publication");if err!=nil{return err};defer cp.Close()
	c,err:=readOperationClosure(cp,cr,ce);if err!=nil||c.Witness!=w{return ErrStageRecovery}
	b,err:=openDir(op,"batch");if err!=nil{return err};defer b.Close()
	v:=BatchItemOutcome{PostID:item.PostID,State:"committed",Receipt:&w}
	raw,err:=json.Marshal(v);if err!=nil||len(raw)>maxRecordBytes{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,b,item.Request.OperationID+".json",raw);err!=nil{return err};syncDirectory(b)
	return nil
}

// CommitBatchForOwner runs the entire approved subset in source order.
// No re-download or automatic retry. On failure ObserveBatch reports
// committed and unresolved items; exact child receipts remain on disk.
// Parent payload is retained (full reservation) for later explicit cleanup.
func(a *Allocator) CommitBatchForOwner(ctx context.Context,parent workerproto.StageAllocation,set *library.Set,owner BatchOwner)(BatchOutcome,error){
	var zero BatchOutcome
	if set==nil{return zero,ErrStageRefused}
	p,manifest,snapshot,err:=a.beginBatch(ctx,parent,owner);if err!=nil{return zero,err}
	var native map[string]json.RawMessage
	if json.Unmarshal(manifest,&native)!=nil{return zero,ErrStageRecovery}
	for i,item:=range p.Items{
		if item.File.ID==""{continue}
		if ctx.Err()!=nil||owner.Current(ctx,parent)!=nil{return a.batchFailure(ctx,parent,ErrStageRefused)}
		row:=snapshot.Rows[i]
		auth,err:=owner.Child(ctx,parent,item.Request,row);if err!=nil{return a.batchFailure(ctx,parent,err)}
		current:=auth.Current
		if current==nil{return a.batchFailure(ctx,parent,ErrStageRefused)}
		auth.Current=func(c context.Context,q workerproto.StageAllocation)error{
			if owner.Current(c,parent)!=nil{return ErrStageRefused}
			return current(c,q)
		}
		one,err:=json.Marshal(map[string]json.RawMessage{item.PostID:native[item.PostID]})
		if err!=nil||digestBytes(one)!=item.Request.Transfer.ManifestSHA256{return a.batchFailure(ctx,parent,ErrStageRecovery)}
		child,err:=a.copyBatchChild(ctx,parent,item,one,row,auth);if err!=nil{return a.batchFailure(ctx,parent,fmt.Errorf("stage post %s: %w",item.PostID,err))}
		w,err:=a.CommitSealedForOwner(ctx,child,set,auth);if err!=nil{return a.batchFailure(ctx,parent,fmt.Errorf("publish post %s: %w",item.PostID,err))}
		if err:=a.persistBatchItem(ctx,parent,p,item,w);err!=nil{return a.batchFailure(ctx,parent,fmt.Errorf("record receipt for post %s: %w",item.PostID,err))}
		if owner.Archive!=nil{
			if _,err:=a.ArchiveClosed(ctx,child,owner.Archive);err!=nil{return a.batchFailure(ctx,parent,fmt.Errorf("archive post %s: %w",item.PostID,err))}
		}
	}
	if owner.Current(ctx,parent)!=nil{return a.batchFailure(ctx,parent,ErrStageRefused)}
	a.mu.Lock();defer a.mu.Unlock()
	op,err:=openDir(a.root,parent.Request.OperationID);if err!=nil{return zero,err};defer op.Close()
	b,err:=openDir(op,"batch");if err!=nil{return zero,err};defer b.Close()
	out,err:=batchOutcome(b,p,false);if err!=nil{return zero,err}
	if err:=a.verifyBatchChildrenLocked(ctx,p,out);err!=nil{return zero,err}
	raw,err:=json.Marshal(out);if err!=nil||len(raw)>maxBatchRecord{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,b,"result.json",raw);err!=nil{return zero,err};syncDirectory(b)
	return readBatchResult(b,p)
}
func(a *Allocator) batchFailure(ctx context.Context,parent workerproto.StageAllocation,cause error)(BatchOutcome,error){
	out,err:=a.ObserveBatch(context.WithoutCancel(ctx),parent)
	return out,errors.Join(cause,err)
}
func(a *Allocator) ObserveBatch(ctx context.Context,parent workerproto.StageAllocation)(BatchOutcome,error){
	a.mu.Lock();defer a.mu.Unlock()
	var zero BatchOutcome
	if ctx.Err()!=nil||!a.live(){return zero,ErrStageRefused}
	if _,ok:=a.history[parent.Request.OperationID];ok{return a.historicalBatchLocked(ctx,parent)}
	r,err:=a.load(parent.Request.OperationID);if err!=nil||r.Allocation!=parent{return zero,ErrStageRefused}
	op,err:=openDir(a.root,parent.Request.OperationID);if err!=nil{return zero,err};defer op.Close()
	e,err:=readSeal(op,parent);if err!=nil{return zero,err}
	p,err:=readBatchPlan(op,r,e);if err!=nil{return zero,err}
	b,err:=openDir(op,"batch");if err!=nil{return zero,err};defer b.Close()
	if _,err:=b.Lstat("result.json");err==nil{
		out,err:=readBatchResult(b,p);if err!=nil{return zero,err}
		if err:=a.verifyBatchChildrenLocked(ctx,p,out);err!=nil{return zero,err}
		return out,nil
	}else if !errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	out,err:=batchOutcome(b,p,true);if err!=nil{return zero,err}
	// A lost aggregate acknowledgement can be reconciled read-only from
	// an exact completed child closure, never by rerunning that child.
	for i,v:=range out.Items{
		if v.State!="unresolved"{continue}
		w,err:=a.batchChildWitnessLocked(ctx,p.Items[i]);if err!=nil{continue}
		out.Items[i]=BatchItemOutcome{PostID:v.PostID,State:"committed",Receipt:&w}
	}
	if err:=a.verifyBatchChildrenLocked(ctx,p,out);err!=nil{return zero,err}
	return out,nil
}

func(a *Allocator) batchChildWitnessLocked(ctx context.Context,item batchItem)(OwnerReceipt,error){
	var zero OwnerReceipt
	if item.File.ID==""||!validID(item.Request.OperationID){return zero,ErrStageRefused}
	var w OwnerReceipt
	if archived,ok:=a.history[item.Request.OperationID];ok{
		h,err:=openDir(a.root,historyDirName);if err!=nil{return zero,err}
		got,err:=readHistoryEntry(ctx,h,item.Request.OperationID,a.path);h.Close()
		if err!=nil||got!=archived{return zero,ErrStageRecovery}
		w=got.Closure.Witness
	}else{
		r,err:=a.load(item.Request.OperationID);if err!=nil||r.Allocation.Request!=item.Request{return zero,ErrStageRecovery}
		done,_,err:=a.closedCharge(ctx,r);if err!=nil||!done{return zero,ErrStageRecovery}
		op,err:=openDir(a.root,item.Request.OperationID);if err!=nil{return zero,err};defer op.Close()
		e,err:=readSeal(op,r.Allocation);if err!=nil||len(e.Files)!=1||e.Files[0]!=item.File{return zero,ErrStageRecovery}
		p,err:=openDir(op,"publication");if err!=nil{return zero,err};defer p.Close()
		c,err:=readOperationClosure(p,r,e);if err!=nil{return zero,err}
		w=c.Witness
	}
	if w.Allocation.Request!=item.Request||w.Row.PostID!=item.PostID||w.Row.SHA256!=item.File.SHA256||w.Row.Size!=item.File.Size||!validDigest(w.ReceiptSHA256){return zero,ErrStageRecovery}
	return w,nil
}
func(a *Allocator) verifyBatchChildrenLocked(ctx context.Context,p batchPlan,out BatchOutcome)error{
	if len(out.Items)!=len(p.Items){return ErrStageRecovery}
	for i,v:=range out.Items{
		item:=p.Items[i]
		if v.PostID!=item.PostID{return ErrStageRecovery}
		if v.State!="committed"{continue}
		w,err:=a.batchChildWitnessLocked(ctx,item)
		if err!=nil||v.Receipt==nil||w!=*v.Receipt{return ErrStageRecovery}
	}
	return nil
}
