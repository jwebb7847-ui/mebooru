package acquisitionstage

import(
	"context"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/logx"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
	"github.com/monbooru/monbooru/internal/library"
)

// OwnedWindow is a library-side integration seam for one approved
// acquisition window. Transfer receives only the exact allocated
// workspace; ConfirmStopped must independently verify actual worker
// and child custody, not a worker success response.
type OwnedWindow struct{
	Authorize func(context.Context,workerproto.StageAllocation)error
	Transfer func(context.Context,acquisitionpolicy.ManagedPeer,workerproto.StageAllocation,string)error
	ConfirmStopped SealObserver
	Policy PolicyObserver
	Batch BatchOwner
	Reclaim BatchReclaimAuthority
}

// RunOwnedWindow binds the actual profile manager, exact online epoch,
// allocation, contained transfer, seal, mapped policy and owner commit.
// No legacy fallback and no automatic replay of an uncertain window.
// The private runtime/queue adapter must supply the real callbacks.
func(a *Allocator) RunOwnedWindow(ctx context.Context,manager *acquisitionpolicy.Manager,set *library.Set,q workerproto.StageAllocateRequest,run OwnedWindow)(BatchOutcome,error){
	var zero BatchOutcome
	if manager==nil||set==nil||run.Authorize==nil||run.Transfer==nil||run.ConfirmStopped==nil||run.Policy==nil||run.Batch.Current==nil||run.Batch.Child==nil||run.Reclaim==nil{return zero,ErrStageRefused}
	lease,err:=manager.AcquireOnline(ctx);if err!=nil{return zero,err};defer lease.Release()
	c:=lease.Context
	grant,err:=a.Allocate(c,q);if err!=nil{return zero,err}
	current:=func(c context.Context,g workerproto.StageAllocation)error{
		if g!=grant||!lease.Current()||run.Authorize(c,g)!=nil{return ErrStageRefused}
		return nil
	}
	if err:=current(c,grant);err!=nil{return zero,err}
	path,err:=a.Workspace(c,grant);if err!=nil{return zero,err}
	if err:=run.Transfer(c,lease.Peer,grant,path);err!=nil{return zero,err}
	return a.CompleteTransferredForOwner(c,lease,set,grant,run)
}

// CompleteTransferredForOwner is the common library-side handoff after
// an already-authorized transfer. The caller holds the original online
// generation lease, not a new lease minted from a worker result.
func(a *Allocator) CompleteTransferredForOwner(c context.Context,lease *acquisitionpolicy.OnlineLease,set *library.Set,grant workerproto.StageAllocation,run OwnedWindow)(BatchOutcome,error){
	var zero BatchOutcome
	if lease==nil||set==nil||run.Authorize==nil||run.ConfirmStopped==nil||run.Policy==nil||run.Batch.Current==nil||run.Batch.Child==nil||run.Reclaim==nil{return zero,ErrStageRefused}
	current:=func(c context.Context,g workerproto.StageAllocation)error{
		if g!=grant||!lease.Current()||run.Authorize(c,g)!=nil{return ErrStageRefused}
		return nil
	}
	if err:=current(c,grant);err!=nil{return zero,err}
	if _,err:=a.Seal(c,grant,func(c context.Context,g workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){
		if err:=current(c,g);err!=nil{return nil,nil,err}
		return run.ConfirmStopped(c,g)
	});err!=nil{return zero,err}
	if err:=a.BindPolicy(c,grant,func(c context.Context,g workerproto.StageAllocation)([]byte,string,error){
		if err:=current(c,g);err!=nil{return nil,"",err}
		return run.Policy(c,g)
	});err!=nil{return zero,err}
	owner:=run.Batch
	owner.Current=func(c context.Context,g workerproto.StageAllocation)error{
		if err:=current(c,g);err!=nil{return err}
		return run.Batch.Current(c,g)
	}
	start:=time.Now()
	out,err:=a.CommitBatchForOwner(c,grant,set,owner)
	if err!=nil{return out,err}
	job:=grant.Request.Transfer.Metadata.JobID
	logx.Infof("managed acquisition job=%d stage=publication duration=%s",job,time.Since(start).Round(time.Millisecond))
	start=time.Now()
	err=a.ReclaimBatch(c,grant,set,func(c context.Context,g workerproto.StageAllocation,v BatchOutcome)error{
		if err:=current(c,g);err!=nil{return err}
		return run.Reclaim(c,g,v)
	},false)
	if err==nil{logx.Infof("managed acquisition job=%d stage=reclamation duration=%s",job,time.Since(start).Round(time.Millisecond))}
	return out,err
}
