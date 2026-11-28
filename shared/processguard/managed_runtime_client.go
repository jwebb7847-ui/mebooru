package processguard

import (
	"context"
	"sync"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// ManagedRuntimeClient provides dormant remote handles to the existing
// metadata and transfer coordinators. The worker-side OperationSet
// tracks these proxy handles only; actual process ownership, command
// compilation and activation remain exclusively in the custodian.
// No argument list is sent over the pipe.
type ManagedRuntimeClient struct{client *workerproto.StreamProbeClient}

func NewManagedRuntimeClient(s *workerproto.Session)(*ManagedRuntimeClient,error){
	c,err:=workerproto.NewStreamProbeClient(s)
	if err!=nil{return nil,err}
	return &ManagedRuntimeClient{client:c},nil
}
func(c *ManagedRuntimeClient) MetadataStarter(resolve workerproto.MetadataPlanRequest)MetadataStarter{
	return func(ctx context.Context,kind workerproto.MetadataKind,_ []string)(StreamProbeChild,error){
		if c==nil||c.client==nil||!workerproto.ValidMetadataPlan(resolve)||resolve.Kind!=workerproto.MetadataStrictResolve{return nil,ErrMetadataOperation}
		q:=resolve
		switch kind{
		case workerproto.MetadataClassify:q.Kind=kind;q.First,q.Last=0,0
		case workerproto.MetadataStrictResolve:
		default:return nil,ErrMetadataOperation
		}
		return &remoteRuntimeChild{client:c.client,ctx:ctx,start:func(ctx context.Context)(workerproto.ProbeOperationRef,error){
			return c.client.StartManagedMetadata(ctx,q)
		}},nil
	}
}
func(c *ManagedRuntimeClient) TransferStarter(grant workerproto.StageAllocation)StrictTransferStarter{
	return func(ctx context.Context,stage StrictTransferStage,_ []string)(StreamProbeChild,error){
		if c==nil||c.client==nil||!workerproto.ValidStageAllocation(grant){return nil,ErrStrictTransferOperation}
		return &remoteRuntimeChild{client:c.client,ctx:ctx,start:func(ctx context.Context)(workerproto.ProbeOperationRef,error){
			return c.client.StartManagedTransfer(ctx,grant,string(stage))
		}},nil
	}
}

type remoteRuntimeChild struct{
	client *workerproto.StreamProbeClient
	ctx context.Context
	start func(context.Context)(workerproto.ProbeOperationRef,error)
	mu sync.Mutex
	ref workerproto.ProbeOperationRef
	offset uint64
	started,stopped,uncertain,reading,eof bool
}
func(c *remoteRuntimeChild) Activate()error{
	c.mu.Lock();defer c.mu.Unlock()
	if c.started||c.stopped||c.ctx.Err()!=nil{return ErrOperationsSealed}
	c.started=true
	ref,err:=c.start(c.ctx)
	if err!=nil||ref.ID==0{c.uncertain=true;return ErrOperationUnconfirmed}
	c.ref=ref
	return nil
}
func(c *remoteRuntimeChild) ReadChunk(ctx context.Context)([]byte,bool,error){
	c.mu.Lock()
	if c.ref.ID==0||c.stopped||c.uncertain||c.reading||c.eof{c.mu.Unlock();return nil,false,ErrOperationsSealed}
	q:=workerproto.StreamCursor{ID:c.ref.ID,Offset:c.offset}
	c.reading=true
	c.mu.Unlock()
	part,err:=c.client.Read(ctx,q)
	c.mu.Lock();defer c.mu.Unlock()
	c.reading=false
	if err!=nil||c.stopped||c.uncertain{return nil,false,ErrOperationsSealed}
	c.offset+=uint64(len(part.Data))
	c.eof=part.EOF
	return part.Data,part.EOF,nil
}
func(c *remoteRuntimeChild) Result(ctx context.Context)(int,[]byte,error){
	c.mu.Lock()
	if c.ref.ID==0||c.stopped||c.uncertain||c.reading||!c.eof{c.mu.Unlock();return -1,nil,ErrOperationsSealed}
	q:=workerproto.StreamCursor{ID:c.ref.ID,Offset:c.offset}
	c.mu.Unlock()
	result,err:=c.client.Finish(ctx,q)
	c.mu.Lock();defer c.mu.Unlock()
	if err!=nil{c.uncertain=true;return -1,nil,ErrOperationUnconfirmed}
	// Finish acknowledges the custodian's actual contained Stop.
	c.stopped=true
	return result.ExitCode,result.Stderr,nil
}
func(c *remoteRuntimeChild) Stop(ctx context.Context)error{
	c.mu.Lock()
	if c.uncertain{c.mu.Unlock();return ErrOperationUnconfirmed}
	if c.stopped||!c.started{c.stopped=true;c.mu.Unlock();return nil}
	ref:=c.ref
	c.mu.Unlock()
	if ref.ID==0{return ErrOperationUnconfirmed}
	err:=c.client.Stop(ctx,ref)
	c.mu.Lock();defer c.mu.Unlock()
	if err!=nil{c.uncertain=true;return ErrOperationUnconfirmed}
	c.stopped=true
	return nil
}


func(c *ManagedRuntimeClient) OpenWindow(ctx context.Context,q workerproto.MetadataPlanRequest)error{
	if c==nil||c.client==nil{return ErrOperationUnconfirmed}
	return c.client.ManagedWindow(ctx,workerproto.OperationWindowOpen,q)
}
func(c *ManagedRuntimeClient) CloseWindow(ctx context.Context,q workerproto.MetadataPlanRequest)error{
	if c==nil||c.client==nil{return ErrOperationUnconfirmed}
	if err:=c.client.ManagedWindow(ctx,workerproto.OperationWindowClose,q);err!=nil{return ErrOperationUnconfirmed}
	return nil
}
