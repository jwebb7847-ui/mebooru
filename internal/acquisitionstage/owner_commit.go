package acquisitionstage

import (
	"context"
	"fmt"

	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// OwnerReceipt is the read-only terminal witness. It is not a custody grant.
type OwnerReceipt = terminalReceiptWitness

const (
	OwnerDispositionNew = "new"
	OwnerDispositionHeld = existingHeld
	OwnerDispositionRestore = existingRestore
)

// OwnerCommit binds local owner decisions; never accept these from a worker.
type OwnerCommit struct{
	Current func(context.Context,workerproto.StageAllocation)error
	Disposition func(context.Context,workerproto.StageAllocation)(string,error)
	Naming func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error)
	Extras func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error)
	Cleanup func(context.Context,workerproto.StageAllocation,OwnerReceipt)error
}

// CommitSealedForOwner accepts a fresh, already verified/bound sealed
// operation. No automatic replay, network handler or scheduler installed.
// The caller must independently prove current writer custody and policy.
func(a *Allocator) CommitSealedForOwner(ctx context.Context,grant workerproto.StageAllocation,set *library.Set,auth OwnerCommit)(OwnerReceipt,error){
	var out terminalReceiptWitness
	if set==nil||auth.Current==nil||auth.Disposition==nil||auth.Naming==nil||auth.Extras==nil||auth.Cleanup==nil{return out,ErrStageRefused}
	err:=set.WithAcquisitionFence(ctx,grant.Request.Transfer.Metadata.Gallery,func(g *library.Gallery,fence func()error)error{
		var err error
		out,err=a.commitSealedGuarded(ctx,grant,g,auth,fence)
		return err
	})
	if err!=nil{return terminalReceiptWitness{},err}
	return out,nil
}

func(a *Allocator) commitSealedGuarded(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth OwnerCommit,fence func()error)(terminalReceiptWitness,error){
	var zero terminalReceiptWitness
	if fence==nil||auth.Current==nil||auth.Disposition==nil||auth.Naming==nil||auth.Extras==nil||auth.Cleanup==nil||g==nil{return zero,ErrStageRefused}
	current:=func()error{
		if ctx.Err()!=nil||fence()!=nil||auth.Current(ctx,grant)!=nil{return ErrStageRefused}
		return nil
	}
	if err:=current();err!=nil{return zero,err}
	kind,err:=auth.Disposition(ctx,grant)
	if err!=nil||(kind!="new"&&kind!=existingHeld&&kind!=existingRestore){return zero,ErrStageRefused}
	check:=func()error{
		if err:=current();err!=nil{return err}
		got,err:=auth.Disposition(ctx,grant)
		if err!=nil||got!=kind{return ErrStageRefused}
		return nil
	}
	naming:=namingAuthority(func(ctx context.Context,q workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){
		if q!=grant||check()!=nil{return gallery.ReceivedNamingRequest{},ErrStageRefused}
		return auth.Naming(ctx,q)
	})
	extras:=extrasAuthority(func(ctx context.Context,q workerproto.StageAllocation)(contract.JobExtras,error){
		if q!=grant||check()!=nil{return contract.JobExtras{},ErrStageRefused}
		return auth.Extras(ctx,q)
	})
	disposition:=existingDispositionAuthority(func(ctx context.Context,q workerproto.StageAllocation)(string,error){
		if q!=grant||check()!=nil{return "",ErrStageRefused}
		return kind,nil
	})
	step:=func(fn func()error)error{
		if err:=check();err!=nil{return err}
		return fn()
	}
	if err:=step(func()error{return a.preparePublication(ctx,grant)});err!=nil{return zero,err}
	var guarded []existingDispositionAuthority
	if kind=="new"{
		plan,err:=a.planNewPublication(ctx,grant,g,naming,extras);if err!=nil{return zero,err}
		if err:=step(func()error{_,err:=a.publishPlannedNew(ctx,grant,g,naming,extras,plan);return err});err!=nil{return zero,err}
		if err:=step(func()error{return a.landPlannedNew(ctx,grant,g,naming,extras)});err!=nil{return zero,err}
		if err:=step(func()error{_,err:=a.applyMappedMetadata(ctx,grant,g);return err});err!=nil{return zero,err}
		if err:=step(func()error{return a.applyRelationships(ctx,grant,g)});err!=nil{return zero,err}
		if plan.Collection!=nil{if err:=step(func()error{return a.applyCollections(ctx,grant,g)});err!=nil{return zero,err}}
		if err:=step(func()error{return a.applyNaming(ctx,grant,g,naming)});err!=nil{return zero,err}
		if err:=step(func()error{return a.applyClientExtras(ctx,grant,g,naming,extras)});err!=nil{return zero,err}
		if err:=step(func()error{_,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);return err});err!=nil{return zero,err}
	}else{
		guarded=[]existingDispositionAuthority{disposition}
		target,err:=a.inspectExistingPublicationTarget(ctx,grant,g,kind,extras);if err!=nil{return zero,fmt.Errorf("verify existing image target: %w",err)}
		if err:=step(func()error{return a.recordExistingTargetPlan(ctx,grant,g,target,disposition,extras)});err!=nil{return zero,fmt.Errorf("record existing image target: %w",err)}
		if err:=step(func()error{return a.publishExistingTarget(ctx,grant,g,disposition,extras,naming)});err!=nil{return zero,fmt.Errorf("link existing image payload: %w",err)}
		if err:=step(func()error{return a.landExistingTarget(ctx,grant,g,disposition,extras,naming)});err!=nil{return zero,fmt.Errorf("land existing image: %w",err)}
		if err:=step(func()error{_,err:=a.applyExistingMetadata(ctx,grant,g,disposition,extras,naming);return err});err!=nil{return zero,fmt.Errorf("verify existing image metadata: %w",err)}
		if kind==existingHeld{
			if err:=step(func()error{return a.disposeExistingHeld(ctx,grant,g,disposition,extras)});err!=nil{return zero,err}
			if err:=step(func()error{_,err:=a.finishExistingHeldReceipt(ctx,grant,g,disposition,extras);return err});err!=nil{return zero,err}
		}else{
			if err:=step(func()error{return a.nameExistingRestore(ctx,grant,g,disposition,extras,naming)});err!=nil{return zero,err}
			if err:=step(func()error{_,err:=a.finishExistingRestoreReceipt(ctx,grant,g,disposition,extras,naming);return err});err!=nil{return zero,err}
		}
	}
	cleanup:=func(ctx context.Context,q workerproto.StageAllocation,w terminalReceiptWitness)error{
		if q!=grant||check()!=nil{return ErrStageRefused}
		return auth.Cleanup(ctx,q,w)
	}
	if _,err:=a.reclaimWorkspace(ctx,grant,g,naming,extras,cleanup,guarded...);err!=nil{return zero,err}
	if _,err:=a.releasePrepared(ctx,grant,g,naming,extras,cleanup,guarded...);err!=nil{return zero,err}
	if _,err:=a.retireSealedMedia(ctx,grant,g,naming,extras,cleanup,guarded...);err!=nil{return zero,err}
	closed,err:=a.closeRetiredOperation(ctx,grant,g,naming,extras,cleanup,guarded...)
	if err!=nil{return zero,err}
	return closed.Witness,nil
}
