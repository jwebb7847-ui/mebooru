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
	generation workerproto.DrainGeneration
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
		identity:acquisitionplan.MetadataIdentity{Runtime:runtime,Config:cfg},generation:gate.CurrentGeneration(),observe:observe,start:start},nil
}

// capture returns a separate cleanup error. A canceled read, EOF or
// process result alone cannot release the lease. Missing-handle startup
// uncertainty permanently poisons the gate, as does failed Stop.
func (o *MetadataOperation) capture(ctx context.Context,kind workerproto.MetadataKind,args []string,limit int)([]byte,error,error){
	return captureOwnedStream(ctx,o.set,func(start context.Context)(StreamProbeChild,error){return o.start(start,kind,args)},limit,o.current)
}
func captureOwnedStream(ctx context.Context,set *OperationSet,startChild StreamProbeStarter,limit int,check func(context.Context)bool) (out []byte, resultErr error, cleanupErr error) {
	var child StreamProbeChild
	lease,err:=set.startChecked(ctx,func(start context.Context)(OperationChild,error){
		var err error
		child,err=startChild(start)
		if child==nil{return nil,err}
		return child,err
	},check)
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
	out,_,err:=o.RunBound(caller,q)
	return out,err
}

// RunBound is a diagnostic compatibility seam, not the preflight handoff.
// RunCompletion is required by the composed candidate.
// RunBound returns the originating opaque online generation with the
// validated native output. A subsequent local taxonomy candidate must
// use AdmitBound on that same gate and token, not acquire a fresh grant
// based on old metadata. Not a wire DTO or durable job authorization.
func (o *MetadataOperation) RunBound(caller context.Context,q workerproto.MetadataPlanRequest)([]byte,workerproto.DrainGeneration,error){
	if o==nil{return nil,workerproto.DrainGeneration{},ErrMetadataOperation}
	completion,err:=o.RunCompletion(caller,q)
	if err!=nil{return nil,workerproto.DrainGeneration{},err}
	return bytes.Clone(completion.state.body),completion.state.generation,nil
}

// MetadataCompletion is an opaque, single-use, in-process handoff of
// actual contained metadata evidence. Copying the outer value does not
// duplicate its use state. Only successful owned execution mints one.
// It is not a wire DTO, durable receipt or production runtime attestation.
type MetadataCompletion struct{state *metadataCompletionState}
type metadataCompletionState struct{
	request workerproto.MetadataPlanRequest
	identity acquisitionplan.MetadataIdentity
	gate *workerproto.DrainGate
	generation workerproto.DrainGeneration
	subcategory string
	body []byte
	used atomic.Bool
}

// Consume refuses mismatched job/source/gallery/window, runtime/config,
// foreign gate or stale online generation. The receiving adapter must
// still use AdmitBound with the returned original generation; this
// check is not permission to start network work.
func(c *MetadataCompletion)Consume(ctx context.Context,q workerproto.MetadataPlanRequest,
	id acquisitionplan.MetadataIdentity,gate *workerproto.DrainGate)(string,[]byte,workerproto.DrainGeneration,error){
	if c==nil||c.state==nil||gate==nil{return "",nil,workerproto.DrainGeneration{},ErrMetadataOperation}
	s:=c.state
	if q!=s.request||id!=s.identity||gate!=s.gate||!s.used.CompareAndSwap(false,true){
		return "",nil,workerproto.DrainGeneration{},ErrMetadataOperation
	}
	if ctx.Err()!=nil||gate.CurrentGeneration()!=s.generation{return "",nil,workerproto.DrainGeneration{},ErrMetadataOperation}
	return s.subcategory,bytes.Clone(s.body),s.generation,nil
}

// RunCompletion keeps request, classification, identity and native bytes
// together until the receiving trusted adapter consumes them.
func (o *MetadataOperation) RunCompletion(caller context.Context,q workerproto.MetadataPlanRequest)(*MetadataCompletion,error){
	if o==nil||o.sequence==nil||q!=o.request||!o.used.CompareAndSwap(false,true){return nil,ErrMetadataOperation}
	defer o.sequence.Invalidate()
	ctx,finish,err:=o.gate.AdmitBound(caller,o.generation)
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
	return &MetadataCompletion{state:&metadataCompletionState{request:q,identity:o.identity,gate:o.gate,generation:o.generation,subcategory:subcategory,body:bytes.Clone(out)}},nil
}
