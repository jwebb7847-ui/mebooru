package pipeline

import(
	"context"
	"encoding/json"
	"errors"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/queue"
)

// SessionJobOwner is the actual worker-side private library transport.
// Prepare and Workspace are trusted local bootstrap functions, never
// wire-supplied paths or a fallback to the legacy HTTP importer.
type SessionJobOwner struct{
	Session func() *workerproto.Session
	PrepareWindow func(context.Context,*queue.Job)(*ManagedWindowRuntime,error)
	Workspace func(context.Context,workerproto.StageAllocation)(string,error)
}
func(o *SessionJobOwner) Prepare(ctx context.Context,j *queue.Job)(*ManagedWindowRuntime,error){
	if o==nil||o.PrepareWindow==nil{return nil,errManagedJob}
	return o.PrepareWindow(ctx,j)
}
func(o *SessionJobOwner) call(ctx context.Context,op workerproto.Operation,body json.RawMessage)(workerproto.Envelope,error){
	if o==nil||o.Session==nil||o.Session()==nil{return workerproto.Envelope{},errManagedJob}
	return o.Session().Call(ctx,op,body)
}
// RegisterWindow requests owner admission of the exact already-running durable job.
// The response confirms only the registration; custody independently claims it.
func(o *SessionJobOwner) RegisterWindow(ctx context.Context,q workerproto.MetadataPlanRequest,intent string)error{
	body,err:=workerproto.EncodeWindowPrepare(workerproto.WindowPrepareRequest{Request:q,IntentID:intent});if err!=nil{return err}
	e,err:=o.call(ctx,workerproto.WindowPrepare,body);if err!=nil||e.Error!=nil{return errors.Join(errManagedJob,err)}
	got,err:=workerproto.DecodeWindowPrepare(e.Payload);if err!=nil||got.Request!=q||got.IntentID!=intent{return errManagedJob};return nil
}
func(o *SessionJobOwner) NoTransfer(ctx context.Context,q workerproto.WindowNoTransferProposal)error{
	body,err:=workerproto.EncodeWindowNoTransfer(q);if err!=nil{return err}
	e,err:=o.call(ctx,workerproto.WindowNoTransfer,body);if err!=nil||e.Error!=nil{return errors.Join(errManagedJob,err)}
	got,err:=workerproto.DecodeManagedMetadataStart(e.Payload);if err!=nil||got!=q.Request{return errManagedJob};return nil
}
func(o *SessionJobOwner) Allocate(ctx context.Context,q workerproto.TransferPlanRequest,projection []byte,hash string)(workerproto.StageAllocation,string,error){
	var zero workerproto.StageAllocation
	if o.Workspace==nil{return zero,"",errManagedJob}
	body,err:=workerproto.EncodeWindowAllocateProposal(workerproto.WindowAllocateProposal{Transfer:q,Projection:projection,PolicySHA256:hash});if err!=nil{return zero,"",err}
	e,err:=o.call(ctx,workerproto.StageAllocate,body)
	if err!=nil||e.Error!=nil{return zero,"",errors.Join(errManagedJob,err)}
	var g workerproto.StageAllocation
	if json.Unmarshal(e.Payload,&g)!=nil||g.Request.Transfer!=q{return zero,"",errManagedJob}
	g,err=workerproto.DecodeStageAllocation(e.Payload,g.Request);if err!=nil{return zero,"",err}
	path,err:=o.Workspace(ctx,g)
	if err!=nil{return zero,"",err}
	return g,path,nil
}
func(o *SessionJobOwner) Commit(ctx context.Context,g workerproto.StageAllocation,hash string)(workerproto.WindowOutcome,error){
	var zero workerproto.WindowOutcome
	body,err:=workerproto.EncodeWindowCommitRequest(workerproto.WindowCommitRequest{Allocation:g,PolicySHA256:hash});if err!=nil{return zero,err}
	e,err:=o.call(ctx,workerproto.ImportMedia,body)
	if err!=nil{return zero,err}
	if e.Error!=nil{return zero,errManagedJob}
	return workerproto.DecodeWindowOutcome(e.Payload,g,hash)
}
var _ ManagedJobOwner=(*SessionJobOwner)(nil)
