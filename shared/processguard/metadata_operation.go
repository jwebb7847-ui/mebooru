package processguard

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

var ErrMetadataOperation = errors.New("contained metadata operation refused")

const metadataClassificationLimit = 64 << 10
const metadataResolveLimit = workerproto.MaxMetadataOutputBytes
const metadataOperationTimeout = 90 * time.Second

// MetadataObserver and MetadataStarter are trusted custody-side seams.
// Observer verifies current runtime/config/generation and job scope.
// Starter accepts only locally compiled arguments and must return a
// dormant, independently owned child under OperationSet's contract.
// ReadChunk/Result must honor context; Stop proves descendant cleanup.
// Neither callback may be populated by requester data.
type MetadataObserver func(context.Context) (acquisitionplan.MetadataIdentity, error)
type MetadataStarter func(context.Context, workerproto.MetadataKind, []string) (StreamProbeChild, error)

// MetadataOperation is a one-use candidate orchestration adapter, not a
// production broker or runtime launcher. One drain lease spans both
// classification and resolution. Output is returned only to trusted
// local code, not over IPC; native envelope validation is not a verdict.
type MetadataOperation struct {
	set *OperationSet
	gate *workerproto.DrainGate
	sequence *acquisitionplan.MetadataSequence
	request workerproto.MetadataPlanRequest
	identity acquisitionplan.MetadataIdentity
	observe MetadataObserver
	start MetadataStarter
	used atomic.Bool
}

func NewMetadataOperation(set *OperationSet, gate *workerproto.DrainGate,
	grant workerproto.MetadataPlanRequest, runtime acquisitionplan.RuntimeIdentity,
	config acquisitionplan.MetadataConfig, observe MetadataObserver, start MetadataStarter) (*MetadataOperation,error) {
	if set==nil || gate==nil || observe==nil || start==nil { return nil,ErrMetadataOperation }
	sequence,err:=acquisitionplan.NewMetadataSequence(grant,runtime,config)
	if err!=nil{return nil,ErrMetadataOperation}
	cfg,err:=config.Identity()
	if err!=nil{return nil,ErrMetadataOperation}
	return &MetadataOperation{set:set,gate:gate,sequence:sequence,request:grant,
		identity:acquisitionplan.MetadataIdentity{Runtime:runtime,Config:cfg},observe:observe,start:start},nil
}

// capture returns a separate cleanup error. A canceled read, EOF or
// process result alone cannot release the lease. Missing-handle startup
// uncertainty permanently poisons the gate, as does failed Stop.
func (o *MetadataOperation) capture(ctx context.Context, kind workerproto.MetadataKind,args []string,limit int) (out []byte, resultErr error, cleanupErr error) {
	var child StreamProbeChild
	lease,err:=o.set.Start(ctx,func(start context.Context)(OperationChild,error){
		var err error
		child,err=o.start(start,kind,args)
		if child==nil{return nil,err}
		return child,err
	})
	if lease!=nil{
		defer func(){
			cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
			defer done()
			if err:=lease.Stop(cleanup);err!=nil{
				out=nil
				resultErr=ErrMetadataOperation
				cleanupErr=ErrOperationUnconfirmed
			}
		}()
	}
	if errors.Is(err,ErrOperationUnconfirmed){
		// Even if an uncertain returned handle is later stopped, refuse
		// the session conservatively until a new custody generation.
		return nil,ErrMetadataOperation,ErrOperationUnconfirmed
	}
	if err!=nil||lease==nil||ctx.Err()!=nil{return nil,ErrMetadataOperation,nil}
	var buf bytes.Buffer
	for {
		if ctx.Err()!=nil{return nil,ErrMetadataOperation,nil}
		data,eof,err:=child.ReadChunk(ctx)
		if err!=nil||ctx.Err()!=nil||len(data)>workerproto.StreamChunkLimit||
			len(data)>limit-buf.Len()||(len(data)==0&&!eof){
			return nil,ErrMetadataOperation,nil
		}
		_,_=buf.Write(data)
		if eof{break}
	}
	exit,stderr,err:=child.Result(ctx)
	// Do not forward raw runtime errors or stderr across this boundary.
	if err!=nil||ctx.Err()!=nil||exit!=0||len(stderr)!=0{return nil,ErrMetadataOperation,nil}
	return buf.Bytes(),nil,nil
}

func (o *MetadataOperation) current(ctx context.Context) bool {
	if ctx.Err()!=nil{return false}
	actual,err:=o.observe(ctx)
	return err==nil&&ctx.Err()==nil&&actual==o.identity
}

// Run consumes a matching grant even on admission denial. A mismatched
// request never acquires it. Trusted owner must create a
// new grant after offline, cancellation, changed config or failed work.
// Cancellation is checked before side effects and publication; Drain
// waits for the entire operation's final cleanup. This is not atomic
// result publication with the library policy transaction.
func (o *MetadataOperation) Run(caller context.Context,q workerproto.MetadataPlanRequest)([]byte,error){
	if o==nil||o.sequence==nil||q!=o.request||!o.used.CompareAndSwap(false,true){return nil,ErrMetadataOperation}
	defer o.sequence.Invalidate()
	ctx,finish,err:=o.gate.Admit(caller)
	if err!=nil{return nil,err}
	var cleanupErr error
	defer func(){finish(cleanupErr)}()
	ctx,cancel:=context.WithTimeout(ctx,metadataOperationTimeout)
	defer cancel()
	if !o.current(ctx)||caller.Err()!=nil{return nil,ErrMetadataOperation}
	classify:=q
	classify.Kind=workerproto.MetadataClassify
	classify.First,classify.Last=0,0
	args,err:=o.sequence.BeginClassification(classify,o.identity)
	if err!=nil{return nil,ErrMetadataOperation}
	classification,err,proofErr:=o.capture(ctx,classify.Kind,args,metadataClassificationLimit)
	cleanupErr=proofErr
	if proofErr!=nil{return nil,ErrOperationUnconfirmed}
	if err!=nil||!o.current(ctx)||caller.Err()!=nil{return nil,ErrMetadataOperation}
	// The attestation is constructed here, only after capture's exact
	// registered OperationLease.Stop returned success.
	if err:=o.sequence.AcceptClassification(acquisitionplan.ClassificationCompletion{
		Request:classify,Observed:o.identity,ExitCode:0,Cleaned:true,Stdout:string(classification),
	});err!=nil{return nil,ErrMetadataOperation}
	subcategory,err:=acquisitionplan.QualifiedClassification(string(classification),q.Source)
	if err!=nil{return nil,ErrMetadataOperation}
	args,err=o.sequence.BeginResolve(q,o.identity)
	if err!=nil||!o.current(ctx)||caller.Err()!=nil{return nil,ErrMetadataOperation}
	out,err,proofErr:=o.capture(ctx,q.Kind,args,metadataResolveLimit)
	cleanupErr=proofErr
	if proofErr!=nil{return nil,ErrOperationUnconfirmed}
	if err!=nil||!o.current(ctx)||caller.Err()!=nil{return nil,ErrMetadataOperation}
	if _,err:=workerproto.DecodeMetadataEvidence(out,q,subcategory);err!=nil{return nil,ErrMetadataOperation}
	if ctx.Err()!=nil||caller.Err()!=nil{return nil,ErrMetadataOperation}
	return out,nil
}
