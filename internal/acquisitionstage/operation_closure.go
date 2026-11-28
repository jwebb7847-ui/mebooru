package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/monbooru/monbooru/internal/library"
)

// Explicit owner acknowledgment of a fully retired operation. The
// historical closure is not a live receipt, a retry capability, or
// permission to delete audit records. Inventory never reopens writers.
type closureAuthority func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error
const maxClosureRecord = 256 << 10
type operationClosure struct{
	Version int `json:"version"`
	Witness terminalReceiptWitness `json:"witness"`
	RetirementResultSHA256 string `json:"retirement_result_sha256"`
	RetainedPayloadBytes uint64 `json:"retained_payload_bytes"`
}
func expectedOperationClosure(p *os.Root,r record,e SealedEvidence)(operationClosure,error){
	var zero operationClosure
	m,raw,err:=readSealedRetirementAttempt(p,r,e)
	if err!=nil{return zero,err}
	result,err:=readSealedRetirementResult(p,m,raw)
	if err!=nil{return zero,err}
	body,err:=json.Marshal(result)
	if err!=nil||result.RetainedSidecarBytes<=0||uint64(result.RetainedSidecarBytes)>r.Allocation.MaxBytes{return zero,ErrStageRecovery}
	return operationClosure{Version:1,Witness:m.Witness,RetirementResultSHA256:digestBytes(body),RetainedPayloadBytes:uint64(result.RetainedSidecarBytes)},nil
}
func readOperationClosure(p *os.Root,r record,e SealedEvidence)(operationClosure,error){
	var zero operationClosure
	want,err:=expectedOperationClosure(p,r,e)
	if err!=nil{return zero,err}
	raw,err:=boundedRegularBytes(p,"operation-closed.json",maxClosureRecord)
	body,e2:=json.Marshal(want)
	if err!=nil||e2!=nil||!bytes.Equal(raw,body){return zero,ErrStageRecovery}
	return want,nil
}

// Historical accounting verifies private retirement evidence, NOT the
// mutable current gallery state. A later legitimate user rename, edit
// or deletion must not retroactively turn a closed operation into a new
// transfer. The synced closure was written only after actual terminal
// reconciliation and explicit owner authorization. Ambiguity refuses.
func(a *Allocator)closedCharge(ctx context.Context,r record)(bool,uint64,error){
	op,err:=openDir(a.root,r.Allocation.Request.OperationID)
	if err!=nil{return false,0,err};defer op.Close()
	if _,err:=op.Lstat("abandoned");err==nil{
		m,err:=readAbandoned(op,r)
		if err!=nil{return false,0,err}
		return true,m.RetainedReservation,nil
	}else if !errors.Is(err,os.ErrNotExist){return false,0,ErrStageRecovery}
	if _,err:=op.Lstat("batch");err==nil{
		e,err:=readSeal(op,r.Allocation);if err!=nil{return false,0,err}
		plan,err:=readBatchPlan(op,r,e);if err!=nil{return false,0,err}
		b,err:=openDir(op,"batch");if err!=nil{return false,0,err};defer b.Close()
		if _,err:=b.Lstat("result.json");errors.Is(err,os.ErrNotExist){return false,r.Allocation.MaxBytes,nil}else if err!=nil{return false,0,ErrStageRecovery}
		out,err:=readBatchResult(b,plan);if err!=nil{return false,0,err}
		if _,err:=b.Lstat("reclaim-result.json");errors.Is(err,os.ErrNotExist){return true,r.Allocation.MaxBytes,nil}else if err!=nil{return false,0,ErrStageRecovery}
		m,err:=readBatchReclaim(b,plan,out,true);if err!=nil{return false,0,err}
		if err:=verifyBatchPrivate(ctx,op,r,e,true,true);err!=nil{return false,0,err}
		return true,m.RetainedSidecarBytes,nil
	}else if !errors.Is(err,os.ErrNotExist){return false,0,ErrStageRecovery}
	if _,err:=op.Lstat("publication");errors.Is(err,os.ErrNotExist){return false,r.Allocation.MaxBytes,nil}else if err!=nil{return false,0,ErrStageRecovery}
	p,err:=openDir(op,"publication")
	if err!=nil{return false,0,err};defer p.Close()
	if _,err:=p.Lstat("operation-closed.json");errors.Is(err,os.ErrNotExist){return false,r.Allocation.MaxBytes,nil}else if err!=nil{return false,0,ErrStageRecovery}
	e,err:=readSeal(op,r.Allocation)
	if err!=nil{return false,0,err}
	if _,err:=readPublication(op,r,e);err!=nil{return false,0,err}
	c,err:=readOperationClosure(p,r,e)
	if err!=nil{return false,0,err}
	m,_,err:=readSealedRetirementAttempt(p,r,e)
	if err!=nil||m.Witness!=c.Witness{return false,0,ErrStageRecovery}
	sealed,err:=openDir(op,"sealed")
	if err!=nil{return false,0,err};defer sealed.Close()
	media,err:=openDir(sealed,"media")
	if err!=nil{return false,0,err};defer media.Close()
	if err:=verifySealedRetirementInventory(ctx,media,m,true,true);err!=nil{return false,0,err}
	manifest,err:=boundedRegularBytes(sealed,"manifest.json",workerproto.MaxMetadataOutputBytes)
	if err!=nil{return false,0,err}
	verifier,err:=acquisitionplan.NewStrictManifestVerifier(r.Allocation.Request.Transfer,manifest)
	if err!=nil{return false,0,ErrStageRecovery}
	snapshot,err:=verifyBoundPolicy(sealed,e,manifest)
	if err!=nil{return false,0,err}
	_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	if err!=nil||hash!=c.Witness.PolicySHA256{return false,0,ErrStageRecovery}
	native,err:=boundedRegularBytes(media,m.Sidecar.Name,workerproto.MaxMetadataPostBytes)
	if err!=nil||digestBytes(native)!=m.Sidecar.SHA256||verifier.VerifySidecar(m.Witness.Row.PostID,native)!=nil{return false,0,ErrStageRecovery}
	if ctx.Err()!=nil||!a.liveSealedMedia(r.Allocation,media){return false,0,ErrStageRefused}
	return true,c.RetainedPayloadBytes,nil
}
func(a *Allocator)closeRetiredOperation(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize closureAuthority,dispositions ...existingDispositionAuthority)(operationClosure,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.closedOperationLocked(ctx,grant,g,naming,extras,authorize,true,dispositions...)
}
func(a *Allocator)observeClosedOperation(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize closureAuthority,dispositions ...existingDispositionAuthority)(operationClosure,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.closedOperationLocked(ctx,grant,g,naming,extras,authorize,false,dispositions...)
}
func(a *Allocator)closedOperationLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize closureAuthority,write bool,dispositions ...existingDispositionAuthority)(operationClosure,error){
	var zero operationClosure
	if authorize==nil{return zero,ErrStageRefused}
	w,err:=a.observeLifecycleWitnessLocked(ctx,grant,g,naming,extras,true,true,dispositions...)
	if err!=nil{return zero,err}
	r,e,p,src,dst,err:=a.publicationRootsMode(ctx,grant,true)
	if err!=nil{return zero,err};defer p.Close();defer src.Close();defer dst.Close()
	want,err:=expectedOperationClosure(p,r,e)
	if err!=nil||want.Witness!=w{return zero,ErrStageRecovery}
	if err:=authorize(ctx,grant,w);err!=nil{return zero,ErrStageRefused}
	again,err:=a.observeLifecycleWitnessLocked(ctx,grant,g,naming,extras,true,true,dispositions...)
	if err!=nil||again!=w||ctx.Err()!=nil||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRecovery}
	current,err:=expectedOperationClosure(p,r,e)
	if err!=nil||current!=want{return zero,ErrStageRecovery}
	if !write{return readOperationClosure(p,r,e)}
	if _,err:=p.Lstat("operation-closed.json");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	body,err:=json.Marshal(want)
	if err!=nil||len(body)>maxClosureRecord{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"operation-closed.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	return a.closedOperationLocked(ctx,grant,g,naming,extras,authorize,false,dispositions...)
}
