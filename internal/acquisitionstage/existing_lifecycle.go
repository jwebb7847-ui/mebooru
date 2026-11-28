package acquisitionstage

import(
	"bytes"
	"context"
	"encoding/json"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// Exactly one explicit authority selects the guarded receipt path.
// No authority supplied selects the unchanged legacy path, never a
// fallback. A nil or ambiguous guarded authority is refused.
func(a *Allocator) observeLifecycleWitnessLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,released,retired bool,dispositions ...existingDispositionAuthority)(terminalReceiptWitness,error){
	if len(dispositions)==0{return a.observeTerminalReceiptPhaseLocked(ctx,grant,g,naming,extras,released,retired)}
	if len(dispositions)!=1||dispositions[0]==nil{return terminalReceiptWitness{},ErrStageRefused}
	return a.observeGuardedExistingWitnessLocked(ctx,grant,g,naming,extras,dispositions[0],released,retired)
}
func guardedWitnessDigest(w terminalReceiptWitness,receipt any)(terminalReceiptWitness,error){
	raw,err:=json.Marshal(receipt)
	if err!=nil||w.ReceiptSHA256!=""||w.Kind!=existingHeldReceiptKind&&w.Kind!=existingRestoreReceiptKind{return terminalReceiptWitness{},ErrStageRecovery}
	w.ReceiptSHA256=digestBytes(raw)
	return w,nil
}
func(a *Allocator) observeGuardedExistingWitnessLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,disposition existingDispositionAuthority,released,retired bool)(terminalReceiptWitness,error){
	var zero terminalReceiptWitness
	if disposition==nil||extras==nil||g==nil||g.WatcherRunning()||retired&&!released{return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRootsMode(ctx,grant,retired)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRecovery}
	plan,err:=readExistingTargetPlan(p,r,e.Files[0]);if err!=nil{return zero,err}
	var witness terminalReceiptWitness
	var raw []byte
	switch plan.Target.Kind{
	case existingHeld:
		ev,err:=a.existingHeldEvidenceModeLocked(ctx,grant,g,disposition,extras,"disposed",released,retired)
		if err!=nil||!sameExistingPublicationTarget(plan.Target,ev.Plan.Target){return zero,ErrStageRecovery}
		got,err:=readExistingHeldReceipt(p,r,ev.Plan,ev.Metadata,ev.Result,ev.Attempt)
		if err!=nil||got!=ev.Receipt{return zero,ErrStageRecovery}
		raw,err=json.Marshal(got);if err!=nil{return zero,ErrStageRecovery}
		witness,err=guardedWitnessDigest(got.Witness,got);if err!=nil{return zero,err}
	case existingRestore:
		ev,err:=a.existingRestoreEvidenceModeLocked(ctx,grant,g,disposition,extras,naming,"named",released,retired)
		if err!=nil||!sameExistingPublicationTarget(plan.Target,ev.Plan.Target){return zero,ErrStageRecovery}
		got,err:=readExistingRestoreReceipt(p,r,ev.Plan,ev.Metadata,ev.Attempt)
		if err!=nil||got!=ev.Receipt{return zero,ErrStageRecovery}
		raw,err=json.Marshal(got);if err!=nil{return zero,ErrStageRecovery}
		witness,err=guardedWitnessDigest(got.Witness,got);if err!=nil{return zero,err}
	default:return zero,ErrStageRecovery
	}
	name,limit:=receiptRecordName(witness.Kind)
	body,err:=boundedRegularBytes(p,name,limit)
	if err!=nil||!bytes.Equal(raw,body)||witness.Allocation!=grant||witness.GalleryIdentity!=r.GalleryIdentity||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRecovery}
	if released{
		m,_,err:=readPreparedReleaseAttempt(p,r,e)
		if err!=nil||m.Witness!=witness||m.Prepared.Identity!=plan.Target.FileIdentity{return zero,ErrStageRecovery}
		if err:=verifyPreparedReleaseInventory(src,m,true);err!=nil{return zero,err}
		if retired{
			m,_,err:=readSealedRetirementAttempt(p,r,e)
			if err!=nil||m.Witness!=witness{return zero,ErrStageRecovery}
		}
	}
	return witness,nil
}
