package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/taxonomy"
)

var ErrManagedPreflight = errors.New("managed strict preflight refused")

// TransferObserver supplies trusted local runtime/config and complete
// rule-policy generation. The owner must increment the generation for
// every relevant policy/category/scope change, never reuse generations.
// This is not requester data or durable authority.
type TransferObserver func(context.Context)(acquisitionplan.MetadataIdentity,uint64,error)

type ManagedPreflight struct{
	window *ManagedEvidenceWindow
	identity acquisitionplan.MetadataIdentity
	config acquisitionplan.MetadataConfig
	policyGeneration uint64
	stamp mapping.StrictPolicyStamp
	observe TransferObserver
	used atomic.Bool
}

func newManagedPreflight(w *ManagedEvidenceWindow,identity acquisitionplan.MetadataIdentity,
	config acquisitionplan.MetadataConfig,policyGeneration uint64,observe TransferObserver)(*ManagedPreflight,error){
	if w==nil||w.mapper==nil||observe==nil||policyGeneration==0||config.Source!=w.grant.Source{return nil,ErrManagedPreflight}
	cfg,err:=config.Identity()
	if err!=nil||cfg!=identity.Config||identity.Runtime.Generation==0||!acquisitionplan.StrictRuntimeQualified(identity.Runtime.Version){return nil,ErrManagedPreflight}
	return &ManagedPreflight{window:w,identity:identity,config:config,policyGeneration:policyGeneration,
		stamp:w.mapper.CaptureStrictPolicy(),observe:observe},nil
}

func(p *ManagedPreflight) current(ctx context.Context)bool{
	w:=p.window
	if ctx.Err()!=nil||!w.scope(ctx,w.grant)||(w.mapper!=nil&&!w.mapper.StrictPolicyMatches(p.stamp)){return false}
	id,generation,err:=p.observe(ctx)
	return err==nil&&ctx.Err()==nil&&id==p.identity&&generation==p.policyGeneration&&(w.mapper==nil||w.mapper.StrictPolicyMatches(p.stamp))
}

type ManagedPreflightResult struct{
	// BlockedIDs are only local outcome evidence, never a wire grant.
	BlockedIDs []string
	ListedIDs []string
	Approval *ManagedTransferApproval
}

// ManagedTransferApproval is deliberately local. It holds the owner
// plan and original gate generation; the request only identifies it.
// The local coordinator constructs all arguments; requester input cannot
// alter the approved list, predicate, runtime or destination.
type ManagedTransferApproval struct{
	preflight *ManagedPreflight
	plan *acquisitionplan.StrictTransferPlan
	projection []byte
	projectionSHA256 string
	used atomic.Bool
}

func(a *ManagedTransferApproval) Request()workerproto.TransferPlanRequest{
	if a==nil||a.plan==nil{return workerproto.TransferPlanRequest{}}
	return a.plan.Request()
}

// Run evaluates the same strict raw/interpreted verdict as legacy
// acquisition, using only the internally produced evidence. There is
// no requester-supplied approval list. A failed evidence window or
// changed policy cannot mint a transfer grant. Empty/all-blocked
// windows do not create an approval. Legacy outcomes are untouched.
func(p *ManagedPreflight) Run(caller context.Context,q workerproto.MetadataPlanRequest)(ManagedPreflightResult,error){
	if p==nil||p.window==nil||q!=p.window.grant||!p.used.CompareAndSwap(false,true){return ManagedPreflightResult{},ErrManagedPreflight}
	w:=p.window
	ctx,finish,err:=w.gate.AdmitBound(caller,w.generation)
	if err!=nil{return ManagedPreflightResult{},err}
	defer finish(nil)
	if !p.current(ctx)||caller.Err()!=nil{return ManagedPreflightResult{},ErrManagedPreflight}
	rules,err:=w.mapper.StrictBlacklist(q.Source)
	if err!=nil{return ManagedPreflightResult{},ErrManagedPreflight}
	rows,err:=w.Run(ctx,q)
	if err!=nil{return ManagedPreflightResult{},err}
	if len(rows)!=len(w.posts)||!p.current(ctx)||caller.Err()!=nil{return ManagedPreflightResult{},ErrManagedPreflight}
	var result ManagedPreflightResult
	approved:=make([]string,0,len(rows))
	projections:=make([]approvedPolicyRow,0,len(rows))
	projectionBytes:=0
	for i,row:=range rows{
		if !p.current(ctx)||caller.Err()!=nil||row.Item.ID!=w.posts[i].ID{return ManagedPreflightResult{},ErrManagedPreflight}
		result.ListedIDs=append(result.ListedIDs,row.Item.ID)
		note,err:=strictListingVerdict(row.Item,rules)
		if err!=nil{return ManagedPreflightResult{},ErrManagedPreflight}
		if note==""{
			note,err=strictInterpretedVerdict(w.mapper,row.Item,row.Interpreted,rules)
			if err!=nil{return ManagedPreflightResult{},ErrManagedPreflight}
		}
		if note!=""{
			result.BlockedIDs=append(result.BlockedIDs,row.Item.ID)
		}else{
			projected,err:=policyRow(w.mapper,row,w.posts[i])
			if err!=nil||!p.current(ctx){return ManagedPreflightResult{},ErrManagedPreflight}
			encoded,err:=json.Marshal(projected)
			if err!=nil||len(encoded)>maxPolicyProjectionBytes-projectionBytes{return ManagedPreflightResult{},ErrManagedPreflight}
			projectionBytes+=len(encoded)
			approved=append(approved,row.Item.ID)
			projections=append(projections,projected)
		}
	}
	if len(approved)>0{
		plan,err:=acquisitionplan.NewStrictTransferPlan(q,w.subcategory,w.posts,approved,p.policyGeneration,p.identity,p.config)
		if err!=nil{return ManagedPreflightResult{},ErrManagedPreflight}
		projection,digest,err:=encodeApprovedPolicy(plan.Request(),projections)
		if err!=nil||!p.current(ctx){return ManagedPreflightResult{},ErrManagedPreflight}
		result.Approval=&ManagedTransferApproval{preflight:p,plan:plan,projection:projection,projectionSHA256:digest}
	}
	if !p.current(ctx)||caller.Err()!=nil{return ManagedPreflightResult{},ErrManagedPreflight}
	return result,nil
}

// admit is a local seam for the future contained transfer coordinator.
// It consumes matching approval once, admits the original generation,
// then rechecks identity and policy before handing off immutable bytes.
// A successful caller MUST hold finish until confirmed cleanup and pass
// any cleanup uncertainty. No production caller exists; do not use
// this as the only launch guard or release it on mere cancellation.
func(a *ManagedTransferApproval) admit(caller context.Context,q workerproto.TransferPlanRequest)(context.Context,[]byte,func(error),error){
	if a==nil||a.plan==nil||a.preflight==nil||q!=a.Request()||!a.used.CompareAndSwap(false,true){return nil,nil,nil,ErrManagedPreflight}
	p:=a.preflight
	w:=p.window
	ctx,finish,err:=w.gate.AdmitBound(caller,w.generation)
	if err!=nil{return nil,nil,nil,err}
	if !p.current(ctx)||caller.Err()!=nil{finish(nil);return nil,nil,nil,ErrManagedPreflight}
	manifest,err:=a.plan.Consume(q,p.identity,p.policyGeneration)
	if err!=nil||!p.current(ctx)||caller.Err()!=nil{
		finish(nil)
		return nil,nil,nil,ErrManagedPreflight
	}
	return ctx,manifest,finish,nil
}

// runTransferCandidate composes preflight's actual one-use grant with
// the isolated contained coordinator. The trusted owner supplies the
// operation set, disposable root and dormant runtime starter. These are
// not worker-controlled values. No production dispatcher calls this.
func(a *ManagedTransferApproval)runTransferCandidate(caller context.Context,q workerproto.TransferPlanRequest,
	set *processguard.OperationSet,root string,start processguard.StrictTransferStarter)([]acquisitionplan.VerifiedTransferFile,error){
	if a==nil||a.preflight==nil||a.plan==nil{return nil,ErrManagedPreflight}
	p:=a.preflight
	return processguard.RunStrictTransfer(caller,set,q,p.config,root,a.admit,p.current,start)
}


// NewManagedPreflightFromCompletion is the only exported constructor
// for the composed preflight candidate. Callers cannot substitute raw
// evidence, a classified subcategory or an independently acquired epoch.
// Scope, runtime observer and policy generation are still trusted
// local owner seams, not production cross-process authority.
func NewManagedPreflightFromCompletion(ctx context.Context,completion *processguard.MetadataCompletion,
	q workerproto.MetadataPlanRequest,identity acquisitionplan.MetadataIdentity,config acquisitionplan.MetadataConfig,
	gate *workerproto.DrainGate,scope taxonomy.ManagedTaxonomyScope,mapper *mapping.Mapper,transport http.RoundTripper,
	policyGeneration uint64,observe TransferObserver)(*ManagedPreflight,error){
	sub,body,generation,err:=completion.Consume(ctx,q,identity,gate)
	if err!=nil{return nil,ErrManagedPreflight}
	window,err:=newManagedEvidenceWindow(q,sub,body,gate,generation,scope,mapper,transport)
	if err!=nil{return nil,err}
	return newManagedPreflight(window,identity,config,policyGeneration,observe)
}

// RunAllocatedTransfer consumes the original one-use approval and uses
// the owner-issued workspace budget. It is deliberately not a generic
// command runner or an alternate legacy download path.
func(a *ManagedTransferApproval) RunAllocatedTransfer(caller context.Context,grant workerproto.StageAllocation,set *processguard.OperationSet,root string,start processguard.StrictTransferStarter)([]acquisitionplan.VerifiedTransferFile,error){
	if a==nil||a.preflight==nil||a.plan==nil||grant.Request.Transfer!=a.Request(){return nil,ErrManagedPreflight}
	p:=a.preflight
	return processguard.RunAllocatedStrictTransfer(caller,set,grant,p.config,root,a.admit,p.current,start,p.identity.Runtime.Version)
}
