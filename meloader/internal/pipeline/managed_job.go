package pipeline

import(
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/outcome"
	"github.com/monbooru/monloader/internal/queue"
	"github.com/monbooru/monloader/internal/taxonomy"
)

// ManagedWindowRuntime is returned by trusted owner/worker setup for the
// actual running durable queue job. Client talks only to its separately
// owned custodian broker; Close reconciles that broker's custody, not
// merely the worker's local pipe. No direct legacy runtime starter.
type ManagedWindowRuntime struct{
	Request workerproto.MetadataPlanRequest
	Identity acquisitionplan.MetadataIdentity
	Config acquisitionplan.MetadataConfig
	PolicyGeneration uint64
	Observe TransferObserver
	Scope taxonomy.ManagedTaxonomyScope
	Mapper *mapping.Mapper
	Transport http.RoundTripper
	Client *processguard.ManagedRuntimeClient
	Close func(context.Context)error
}

// ManagedJobOwner binds the actual durable job and authoritative policy
// to exact allocation/commit. The local projection artifact is proposed
// evidence, not permission: the library must independently validate and
// bind it before registering the transfer with its custodian. Commit
// returns observed durable receipts, not a worker-side upload result.
type ManagedJobOwner interface{
	Prepare(context.Context,*queue.Job)(*ManagedWindowRuntime,error)
	Allocate(context.Context,workerproto.TransferPlanRequest,[]byte,string)(workerproto.StageAllocation,string,error)
	Commit(context.Context,workerproto.StageAllocation,string)(workerproto.WindowOutcome,error)
}

type ManagedJobProcessor struct{
	Gate *workerproto.DrainGate
	Owner ManagedJobOwner
}

var errManagedJob=errors.New("managed acquisition job refused; reconcile any uncertain operation")

func(p *ManagedJobProcessor) ProcessManaged(ctx context.Context,j *queue.Job)(resultErr,cleanupErr error){
	if p==nil||p.Gate==nil||p.Owner==nil||j==nil||ctx.Err()!=nil{return errManagedJob,nil}
	job:=j.Snapshot()
	// Initial managed path supports strict download windows only.
	// Unsupported metadata, replacement, hash and contribution jobs
	// refuse before WAN rather than falling through to legacy.
	if job.Kind!=""||workerproto.StrictQueueSource(job.URL)==""{return errManagedJob,nil}
	r,err:=p.Owner.Prepare(ctx,job)
	if r==nil{return errors.Join(errManagedJob,err),nil}
	proxies:=processguard.NewOperationSet(1)
	defer func(){
		cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),15*time.Second);defer cancel()
		cleanupErr=errors.Join(cleanupErr,proxies.SealAndStop(cleanup))
		if r.Close==nil{cleanupErr=errors.Join(cleanupErr,processguard.ErrOperationUnconfirmed)}else{cleanupErr=errors.Join(cleanupErr,r.Close(cleanup))}
	}()
	if err!=nil{return err,nil}
	q:=r.Request
	if r.Client==nil||r.Mapper==nil||r.Observe==nil||r.Scope==nil||r.Close==nil||
		!workerproto.ValidMetadataPlan(q)||q.Kind!=workerproto.MetadataStrictResolve||
		q.JobID!=job.ID||q.URL!=job.URL||q.Source!=workerproto.StrictQueueSource(job.URL)||
		job.Gallery!=""&&job.Gallery!=q.Gallery||job.Offset<0||uint64(job.Offset)+1!=uint64(q.First)||
		job.MaxItems>0&&uint64(q.Last-q.First+1)>uint64(job.MaxItems){return errManagedJob,nil}
	j.SetGallery(q.Gallery);j.SetSite(q.Source)
	observe:=func(c context.Context)(acquisitionplan.MetadataIdentity,error){
		if !r.Scope(c,q){return acquisitionplan.MetadataIdentity{},errManagedJob}
		id,generation,err:=r.Observe(c)
		if err!=nil||generation!=r.PolicyGeneration{return acquisitionplan.MetadataIdentity{},errManagedJob}
		return id,nil
	}
	metadata,err:=processguard.NewMetadataOperation(proxies,p.Gate,q,r.Identity.Runtime,r.Config,observe,r.Client.MetadataStarter(q))
	if err!=nil{return err,nil}
	completion,err:=metadata.RunCompletion(ctx,q)
	if err!=nil{return err,nil}
	preflight,err:=NewManagedPreflightFromCompletion(ctx,completion,q,r.Identity,r.Config,p.Gate,r.Scope,r.Mapper,r.Transport,r.PolicyGeneration,r.Observe)
	if err!=nil{return err,nil}
	verdict,err:=preflight.Run(ctx,q)
	if err!=nil{return err,nil}
	blocked:=map[string]bool{}
	for _,id:=range verdict.BlockedIDs{blocked[id]=true}
	items:=make([]queue.Item,len(verdict.ListedIDs))
	for i,id:=range verdict.ListedIDs{
		item:=queue.Item{PostID:id,Num:int(q.First)+i,Status:queue.ItemPending}
		if blocked[id]{item.Status=queue.ItemSkipped;item.Outcome=outcome.SkippedBlacklist}
		items[i]=item
	}
	j.SetItems(items)
	if verdict.Approval==nil{return nil,nil}
	approval:=verdict.Approval
	plan:=approval.Request()
	j.SetSubcategory(plan.Subcategory)
	body,hash,err:=approval.policyEvidence(ctx,plan)
	if err!=nil{return err,nil}
	var projection acquisitionplan.ApprovedPolicySnapshot
	if json.Unmarshal(body,&projection)!=nil||projection.Transfer!=plan{return errManagedJob,nil}
	grant,root,err:=p.Owner.Allocate(ctx,plan,body,hash)
	if err!=nil||!workerproto.ValidStageAllocation(grant)||grant.Request.Transfer!=plan{return errors.Join(errManagedJob,err),nil}
	files,err:=approval.RunAllocatedTransfer(ctx,grant,proxies,root,r.Client.TransferStarter(grant))
	if err!=nil{return err,nil}
	if _,again,err:=approval.policyEvidence(ctx,plan);err!=nil||again!=hash{return errManagedJob,nil}
	observed,commitErr:=p.Owner.Commit(ctx,grant,hash)
	if !workerproto.ValidWindowOutcome(observed,grant,hash){return errors.Join(errManagedJob,commitErr),nil}
	mapped,err:=mapManagedWindowOutcome(items,projection,files,observed)
	if err!=nil{return err,nil}
	j.SetItems(mapped)
	if commitErr!=nil{j.CutShort(outcome.ErrCodeMonbooruRejected,"managed acquisition requires reconciliation")}
	// Partial per-item receipts are retained as such. No automatic retry
	// of unresolved operations and no false blacklist/archive outcome.
	return nil,nil
}

func mapManagedWindowOutcome(items []queue.Item,projection acquisitionplan.ApprovedPolicySnapshot,files []acquisitionplan.VerifiedTransferFile,result workerproto.WindowOutcome)([]queue.Item,error){
	if len(result.Items)!=len(projection.Rows){return nil,errManagedJob}
	transferred:=map[string]acquisitionplan.VerifiedTransferFile{}
	for _,f:=range files{if _,exists:=transferred[f.ID];exists{return nil,errManagedJob};transferred[f.ID]=f}
	observed:=map[string]workerproto.WindowItemOutcome{}
	for i,v:=range result.Items{
		if projection.Rows[i].PostID!=v.PostID{return nil,errManagedJob}
		f,exists:=transferred[v.PostID]
		if exists!=(v.State!="not_transferred")||v.State=="committed"&&v.SHA256!=f.SHA256{return nil,errManagedJob}
		delete(transferred,v.PostID)
		observed[v.PostID]=v
	}
	if len(transferred)!=0{return nil,errManagedJob}
	out:=append([]queue.Item(nil),items...)
	for i:=range out{
		it:=&out[i]
		if it.Outcome==outcome.SkippedBlacklist{continue}
		v,ok:=observed[it.PostID];if !ok{return nil,errManagedJob}
		delete(observed,it.PostID)
		switch v.State{
		case "committed":
			it.Status=queue.ItemDone;it.MonbooruID=v.ImageID;it.SHA256=v.SHA256
			switch v.Disposition{
			case "new":it.Outcome=outcome.Created
			case "held":it.Outcome=outcome.Duplicate;it.MergeNote="Verified existing media and merged source"
			case "restored":it.Outcome=outcome.Duplicate;it.MergeNote="Restored missing media"
			default:return nil,errManagedJob
			}
		case "not_transferred":
			it.Status=queue.ItemFailed;it.Outcome=outcome.Failed;it.ErrorCode=outcome.ErrCodeDownloadFailed;it.Error="Approved post not returned by verified transfer"
		case "unresolved":
			it.Status=queue.ItemFailed;it.Outcome=outcome.Failed;it.ErrorCode=outcome.ErrCodeMonbooruRejected;it.Error="Managed operation requires reconciliation"
		default:return nil,errManagedJob
		}
	}
	if len(observed)!=0{return nil,errManagedJob}
	return out,nil
}

var _ queue.ManagedProcessor=(*ManagedJobProcessor)(nil)
