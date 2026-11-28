package processguard

import(
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

var ErrStrictTransferOperation=errors.New("contained strict transfer refused")

type StrictTransferStage string
const(
	TransferVersion StrictTransferStage="version"
	TransferNegative StrictTransferStage="negative"
	TransferPositive StrictTransferStage="positive"
	TransferMedia StrictTransferStage="media"
)

// StrictTransferAdmission and Current are trusted local authority seams.
// Admit must consume the exact approval, acquire the original generation
// lease, and return immutable manifest bytes. Current must independently
// verify current runtime/config, policy and job scope. The caller holds
// the lease through all probes, media execution and cleanup.
type StrictTransferAdmission func(context.Context,workerproto.TransferPlanRequest)(context.Context,[]byte,func(error),error)
type StrictTransferCurrent func(context.Context)bool
type StrictTransferStarter func(context.Context,StrictTransferStage,[]string)(StreamProbeChild,error)

// RunStrictTransfer is an isolated QA coordinator, not a production
// dispatcher. root is a fresh trusted disposable directory, not a
// worker path. Returned file evidence is not a durable receipt or ingest grant.
// Production staging, real policy authority and atomic
// launch/revocation remain required before production integration.
func RunStrictTransfer(caller context.Context,set *OperationSet,q workerproto.TransferPlanRequest,
	config acquisitionplan.MetadataConfig,root string,admit StrictTransferAdmission,current StrictTransferCurrent,start StrictTransferStarter)([]acquisitionplan.VerifiedTransferFile,error){
	return runStrictTransfer(caller,set,q,config,root,admit,current,start,nil)
}

// RunAllocatedStrictTransfer applies the owner-issued allocation's
// payload and file limits while the actual contained child is running.
// The caller still owns workspace/custody admission; this is a local
// seam, not a wire grant or a hard kernel filesystem quota.
func RunAllocatedStrictTransfer(caller context.Context,set *OperationSet,grant workerproto.StageAllocation,
	config acquisitionplan.MetadataConfig,root string,admit StrictTransferAdmission,current StrictTransferCurrent,start StrictTransferStarter)([]acquisitionplan.VerifiedTransferFile,error){
	if !workerproto.ValidStageAllocation(grant){return nil,ErrStrictTransferOperation}
	return runStrictTransfer(caller,set,grant.Request.Transfer,config,root,admit,current,start,&grant)
}

func runStrictTransfer(caller context.Context,set *OperationSet,q workerproto.TransferPlanRequest,
	config acquisitionplan.MetadataConfig,root string,admit StrictTransferAdmission,current StrictTransferCurrent,start StrictTransferStarter,grant *workerproto.StageAllocation)([]acquisitionplan.VerifiedTransferFile,error){
	if set==nil||admit==nil||current==nil||start==nil||!workerproto.ValidTransferPlan(q){return nil,ErrStrictTransferOperation}
	ctx,manifest,finish,err:=admit(caller,q)
	if err!=nil{return nil,err}
	if finish==nil{return nil,ErrStrictTransferOperation}
	var cleanupErr error
	defer func(){finish(cleanupErr)}()
	if ctx==nil{return nil,ErrStrictTransferOperation}
	ctx,cancel:=context.WithTimeout(ctx,90*time.Second)
	defer cancel()
	allowed:=func()bool{return ctx.Err()==nil&&caller.Err()==nil&&current(ctx)&&ctx.Err()==nil&&caller.Err()==nil}
	if !allowed(){return nil,ErrStrictTransferOperation}
	commands,err:=acquisitionplan.PrepareTransferCommands(q,manifest,config,root)
	if err!=nil{return nil,ErrStrictTransferOperation}
	var budget *transferWorkspaceBudget
	if grant!=nil{
		budget,err=openTransferWorkspaceBudget(root,*grant)
		if err!=nil{return nil,ErrStrictTransferOperation}
		defer budget.Close()
		// Sample even when the child has not produced stdout. Cancellation
		// reaches captureOwnedStream; its deferred Stop proves cleanup.
		// Stop the monitor before closing the retained root handles.
		done:=make(chan struct{})
		stop:=make(chan struct{})
		go func(){defer close(done);budget.monitor(ctx,stop,cancel)}()
		defer func(){close(stop);<-done}()
	}
	withinBudget:=func()bool{return budget==nil||budget.check(ctx)}
	for _,step:=range []struct{stage StrictTransferStage;args []string}{
		{TransferVersion,commands.Version},{TransferNegative,commands.Negative},{TransferPositive,commands.Positive},{TransferMedia,commands.Transfer},
	}{
		if !allowed()||!commands.VerifyFiles()||!withinBudget(){return nil,ErrStrictTransferOperation}
		limit:=64<<10
		if step.stage==TransferMedia{limit=workerproto.MaxMetadataOutputBytes}
		out,err,proofErr:=captureOwnedStream(ctx,set,func(c context.Context)(StreamProbeChild,error){
			if !allowed()||!commands.VerifyFiles()||!withinBudget(){return nil,ErrStrictTransferOperation}
			return start(c,step.stage,step.args)
		},limit,func(context.Context)bool{return allowed()&&commands.VerifyFiles()&&withinBudget()})
		cleanupErr=proofErr
		if proofErr!=nil{return nil,ErrOperationUnconfirmed}
		if err!=nil||!allowed()||!commands.VerifyFiles()||!withinBudget(){return nil,ErrStrictTransferOperation}
		switch step.stage{
		case TransferVersion:
			if strings.TrimSpace(string(out))!=acquisitionplan.StrictQualifiedVersion{return nil,ErrStrictTransferOperation}
		case TransferNegative:
			if !acquisitionplan.ValidatePredicateProbe(out,false){return nil,ErrStrictTransferOperation}
		case TransferPositive:
			if !acquisitionplan.ValidatePredicateProbe(out,true){return nil,ErrStrictTransferOperation}
		case TransferMedia:
			files,err:=commands.VerifyTransferResult(ctx,out)
			if err!=nil||!allowed()||!withinBudget()||grant!=nil&&!allocatedTransferResultFits(*grant,files){return nil,ErrStrictTransferOperation}
			return files,nil
		}
	}
	return nil,ErrStrictTransferOperation
}
