package processguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// ManagedRuntimeGrant is registered by trusted custody-side setup for
// one exact job window. The worker cannot supply its runtime identity,
// approved transfer, workspace, or command line. Current and Transfer
// must independently check owner policy and lifecycle. Complete persists
// the exact verified result only AFTER containment Stop succeeds.
type ManagedRuntimeGrant struct {
	Metadata workerproto.MetadataPlanRequest
	Runtime acquisitionplan.RuntimeIdentity
	Config acquisitionplan.MetadataConfig
	Current func(context.Context)bool
	Transfer func(context.Context)(workerproto.StageAllocation,string,error)
	Start func(context.Context,[]string)(StreamProbeChild,error)
	// Resolve may do source policy evaluation inside the same containment.
	StartResolve func(context.Context,[]string,string)(StreamProbeChild,error)
	Complete func(context.Context,workerproto.StageAllocation,[]acquisitionplan.VerifiedTransferFile)error
	// MetadataComplete observes exact native resolution only after actual Stop.
	// It lets independent library authority validate the worker projection.
	MetadataComplete func(context.Context,workerproto.MetadataPlanRequest,string,[]byte)error
}

type managedRuntimeSession struct{
	mu sync.Mutex
	grant ManagedRuntimeGrant
	identity acquisitionplan.MetadataIdentity
	sequence *acquisitionplan.MetadataSequence
	phase int
	active,failed bool
	subcategory string
	allocation workerproto.StageAllocation
	root string
	lifetime context.Context
	revoke context.CancelFunc
}

func newManagedRuntimeSession(ctx context.Context,revoke context.CancelFunc,g ManagedRuntimeGrant)(*managedRuntimeSession,error){
	if g.Current==nil||g.Transfer==nil||g.Start==nil||g.Complete==nil{return nil,ErrOperationUnconfirmed}
	sequence,err:=acquisitionplan.NewMetadataSequence(g.Metadata,g.Runtime,g.Config);if err!=nil{return nil,err}
	ci,err:=g.Config.Identity();if err!=nil{return nil,err}
	return &managedRuntimeSession{grant:g,sequence:sequence,identity:acquisitionplan.MetadataIdentity{Runtime:g.Runtime,Config:ci},lifetime:ctx,revoke:revoke},nil
}

func(s *managedRuntimeSession) allowed(ctx context.Context)bool{
	return ctx.Err()==nil&&s.lifetime.Err()==nil&&s.grant.Current(ctx)
}

func(s *managedRuntimeSession) begin(ctx context.Context,op workerproto.Operation,body []byte)(StreamProbeChild,error){
	s.mu.Lock();defer s.mu.Unlock()
	if s.failed||s.active||s.phase>=6||!s.allowed(ctx){return nil,ErrOperationsSealed}
	phase:=s.phase
	var args []string
	var err error
	var commands acquisitionplan.TransferCommands
	var budget *transferWorkspaceBudget
	q:=s.grant.Metadata
	if phase<2{
		if op!=workerproto.OperationMetadataStart{return nil,ErrOperationsSealed}
		if phase==0{q.Kind=workerproto.MetadataClassify;q.First,q.Last=0,0}
		request,e:=workerproto.DecodeManagedMetadataStart(body);if e!=nil||request!=q{return nil,ErrOperationsSealed}
		if phase==0{args,err=s.sequence.BeginClassification(q,s.identity)}else{args,err=s.sequence.BeginResolve(q,s.identity)}
	}else{
		if op!=workerproto.OperationStrictTransferStart{return nil,ErrOperationsSealed}
		grant,root,e:=s.grant.Transfer(ctx)
		if e!=nil||!workerproto.ValidStageAllocation(grant)||grant.Request.Transfer.Metadata!=s.grant.Metadata{return nil,ErrOperationsSealed}
		if phase>2&&(grant!=s.allocation||root!=s.root){return nil,ErrOperationsSealed}
		req,e:=workerproto.DecodeManagedTransferStart(body,grant.Request)
		stage:=[]StrictTransferStage{TransferVersion,TransferNegative,TransferPositive,TransferMedia}[phase-2]
		if e!=nil||req.Allocation!=grant||req.Stage!=string(stage){return nil,ErrOperationsSealed}
		commands,err=acquisitionplan.ObservePreparedTransferCommands(grant.Request.Transfer,s.grant.Config,root)
		if err!=nil{return nil,err}
		s.allocation,s.root=grant,root
		budget,err=openTransferWorkspaceBudget(root,grant)
		if err!=nil{return nil,err}
		if !budget.check(ctx){budget.Close();return nil,ErrStrictTransferOperation}
		switch stage{case TransferVersion:args=commands.Version;case TransferNegative:args=commands.Negative;case TransferPositive:args=commands.Positive;case TransferMedia:args=commands.Transfer}
	}
	if err!=nil||!s.allowed(ctx){
		if budget!=nil{budget.Close()}
		s.failed=true
		return nil,ErrOperationsSealed
	}
	s.active=true
	var child StreamProbeChild
	if phase==1&&s.grant.StartResolve!=nil{
		child,err=s.grant.StartResolve(ctx,args,s.subcategory)
	}else{
		child,err=s.grant.Start(ctx,args)
	}
	if child==nil{
		if budget!=nil{budget.Close()}
		s.active=false;s.failed=true
		return nil,err
	}
	limit:=64<<10
	if phase==1||phase==5{limit=workerproto.MaxMetadataOutputBytes}
	return &managedRuntimeChild{StreamProbeChild:child,session:s,phase:phase,request:q,commands:commands,budget:budget,limit:limit},err
}

type managedRuntimeChild struct{
	StreamProbeChild
	session *managedRuntimeSession
	phase int
	request workerproto.MetadataPlanRequest
	commands acquisitionplan.TransferCommands
	budget *transferWorkspaceBudget
	limit int
	mu sync.Mutex
	buf bytes.Buffer
	valid bool
	files []acquisitionplan.VerifiedTransferFile
	stopMonitor,monitorDone chan struct{}
	stopped bool
}

func(c *managedRuntimeChild) Activate()error{
	if !c.session.allowed(c.session.lifetime){return ErrOperationsSealed}
	if c.budget!=nil{
		if !c.budget.check(c.session.lifetime)||!c.commands.VerifyFiles(){return ErrStrictTransferOperation}
		c.stopMonitor=make(chan struct{});c.monitorDone=make(chan struct{})
		go func(){defer close(c.monitorDone);c.budget.monitor(c.session.lifetime,c.stopMonitor,c.session.revoke)}()
	}
	return c.StreamProbeChild.Activate()
}
func(c *managedRuntimeChild) ReadChunk(ctx context.Context)([]byte,bool,error){
	if !c.session.allowed(ctx)||c.budget!=nil&&(!c.budget.check(ctx)||!c.commands.VerifyFiles()){return nil,false,ErrOperationsSealed}
	data,eof,err:=c.StreamProbeChild.ReadChunk(ctx)
	c.mu.Lock();defer c.mu.Unlock()
	if err!=nil||c.stopped||len(data)>c.limit-c.buf.Len(){return nil,false,ErrOperationsSealed}
	c.buf.Write(data)
	return data,eof,nil
}
func(c *managedRuntimeChild) Result(ctx context.Context)(int,[]byte,error){
	exit,stderr,err:=c.StreamProbeChild.Result(ctx)
	c.mu.Lock();defer c.mu.Unlock()
	if err!=nil||exit!=0||len(stderr)!=0||c.stopped||!c.session.allowed(ctx){return exit,stderr,ErrOperationsSealed}
	out:=c.buf.Bytes()
	s:=c.session
	switch c.phase{
	case 0:
		if _,err:=acquisitionplan.QualifiedClassification(string(out),c.request.Source);err!=nil{return exit,stderr,err}
	case 1:
		if _,err:=workerproto.DecodeMetadataEvidence(out,c.request,s.subcategory);err!=nil{return exit,stderr,err}
	case 2:
		if strings.TrimSpace(string(out))!=s.identity.Runtime.Version{return exit,stderr,ErrStrictTransferOperation}
	case 3,4:
		if !acquisitionplan.ValidatePredicateProbe(out,c.phase==4){return exit,stderr,ErrStrictTransferOperation}
	case 5:
		files,err:=c.commands.VerifyTransferResult(ctx,out)
		if err!=nil||!allocatedTransferResultFits(s.allocation,files){return exit,stderr,ErrStrictTransferOperation}
		c.files=files
	}
	if c.budget!=nil&&(!c.budget.check(ctx)||!c.commands.VerifyFiles()){return exit,stderr,ErrStrictTransferOperation}
	c.valid=true
	return exit,stderr,nil
}
func(c *managedRuntimeChild) Stop(ctx context.Context)error{
	// Stop is called by the exact registered lease, independently of
	// caller cancellation; a failed Stop retains actual custody.
	if err:=c.StreamProbeChild.Stop(ctx);err!=nil{return err}
	c.mu.Lock();defer c.mu.Unlock()
	if c.stopped{return nil}
	c.stopped=true
	if c.stopMonitor!=nil{close(c.stopMonitor);<-c.monitorDone}
	if c.budget!=nil{defer c.budget.Close()}
	s:=c.session
	s.mu.Lock();defer s.mu.Unlock()
	s.active=false
	if !c.valid||s.failed||s.phase!=c.phase||!s.allowed(ctx){
		s.failed=true;s.sequence.Invalidate();return nil
	}
	if c.budget!=nil&&(!c.budget.check(ctx)||!c.commands.VerifyFiles()){
		s.failed=true;return ErrStrictTransferOperation
	}
	if c.phase==0{
		out:=string(c.buf.Bytes())
		sub,err:=acquisitionplan.QualifiedClassification(out,c.request.Source)
		if err!=nil||s.sequence.AcceptClassification(acquisitionplan.ClassificationCompletion{Request:c.request,Observed:s.identity,ExitCode:0,Cleaned:true,Stdout:out})!=nil{
			s.failed=true;return ErrMetadataOperation
		}
		s.subcategory=sub
	}
	if c.phase==1&&s.grant.MetadataComplete!=nil{
		if err:=s.grant.MetadataComplete(ctx,s.grant.Metadata,s.subcategory,bytes.Clone(c.buf.Bytes()));err!=nil{s.failed=true;return err}
	}
	if c.phase==5{
		// Re-verify the actual bytes after the contained child stopped.
		files,err:=c.commands.VerifyTransferResult(ctx,c.buf.Bytes())
		if err!=nil||!allocatedTransferResultFits(s.allocation,files){s.failed=true;return ErrStrictTransferOperation}
		if err:=s.grant.Complete(ctx,s.allocation,files);err!=nil{s.failed=true;return err}
	}
	s.phase++
	return nil
}

func(b *probeBroker) startManagedRuntime(ctx context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
	b.mu.Lock();defer b.mu.Unlock()
	if b.sealed||b.next==^uint64(0)||b.managed==nil{return streamFault("closed")}
	var child StreamProbeChild
	lease,err:=b.set.startChecked(ctx,func(c context.Context)(OperationChild,error){
		var e error
		child,e=b.managed.begin(c,op,body)
		if child==nil{return nil,e};return child,e
	},b.managed.allowed)
	if err!=nil||ctx.Err()!=nil{
		if lease!=nil{
			cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
			stopErr:=lease.Stop(cleanup);cancel()
			if stopErr!=nil{b.sealed=true;b.revoke();return streamFault("cleanup_unconfirmed")}
		}
		if errors.Is(err,ErrOperationUnconfirmed){b.sealed=true;b.revoke();return streamFault("cleanup_unconfirmed")}
		return streamFault(workerproto.ManagedStartRefused)
	}
	b.next++
	b.streams[b.next]=&streamProbe{lease:lease,child:child}
	raw,_:=json.Marshal(workerproto.ProbeOperationRef{ID:b.next})
	return raw,nil
}

// ServeManagedRuntimeBroker is an actual typed custody dispatcher for
// a single owner-bound acquisition window. No diagnostic/raw command
// start operation is admitted. The caller must combine its cleanup
// with the primary worker's containment before releasing ownership.
func ServeManagedRuntimeBroker(ctx context.Context,r io.ReadCloser,w io.WriteCloser,set *OperationSet,grant ManagedRuntimeGrant)error{
	if set==nil{r.Close();w.Close();return ErrOperationUnconfirmed}
	lifetime,revoke:=context.WithCancel(ctx);defer revoke()
	m,err:=newManagedRuntimeSession(lifetime,revoke,grant)
	if err!=nil{r.Close();w.Close();return err}
	b:=&probeBroker{set:set,managed:m,managedOnly:true,streams:map[uint64]*streamProbe{},leases:map[uint64]*OperationLease{},revoke:revoke}
	h:=func(c context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
		switch op{
		case workerproto.OperationMetadataStart,workerproto.OperationStrictTransferStart:return b.startManagedRuntime(c,op,body)
		case workerproto.OperationStreamRead,workerproto.OperationStreamFinish,workerproto.OperationStreamStop:return b.handleStream(c,op,body)
		default:return streamFault("unsupported")
		}
	}
	session,err:=workerproto.OpenSession(lifetime,r,w,workerproto.OperationCustodian,h)
	if err==nil{<-session.Done();session.Close()}
	cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout);defer cancel()
	return set.SealAndStop(cleanup)
}


// ManagedWindowResolver is custodian-owned. It consumes an independently
// issued exact job-window grant; no requester-supplied path/config/runtime.
// A closed grant is never reusable, including across worker restarts.
// Settle must independently reconcile the owner outcome (or explicit
// no-transfer/abandonment), before allowing another window.
type ManagedWindowResolver interface{
	Claim(context.Context,workerproto.MetadataPlanRequest)(ManagedRuntimeGrant,error)
	Settle(context.Context,workerproto.MetadataPlanRequest,int,bool)error
}

// ServeManagedRuntimeWindows supports sequential authorized jobs on a
// single private custody session. There is one active window at a time,
// never a general command runner. All stream IDs remain monotonically
// unique for this session. Ambiguous cleanup seals the entire custodian.
func ServeManagedRuntimeWindows(ctx context.Context,r io.ReadCloser,w io.WriteCloser,set *OperationSet,resolver ManagedWindowResolver)error{
	if set==nil||resolver==nil{r.Close();w.Close();return ErrOperationUnconfirmed}
	lifetime,revoke:=context.WithCancel(ctx);defer revoke()
	b:=&probeBroker{set:set,managedOnly:true,streams:map[uint64]*streamProbe{},leases:map[uint64]*OperationLease{},revoke:revoke}
	h:=func(c context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
		switch op{
		case workerproto.OperationWindowOpen,workerproto.OperationWindowClose:
			q,err:=workerproto.DecodeManagedMetadataStart(body)
			if err!=nil||q.Kind!=workerproto.MetadataStrictResolve{return streamFault("invalid")}
			b.mu.Lock();defer b.mu.Unlock()
			if b.sealed||c.Err()!=nil||len(b.streams)!=0{return streamFault("busy")}
			switch op{
			case workerproto.OperationWindowOpen:
				if b.managed!=nil{return streamFault("busy")}
				g,err:=resolver.Claim(c,q)
				if err!=nil||g.Metadata!=q||g.Current==nil||!g.Current(c){return streamFault("refused")}
				m,err:=newManagedRuntimeSession(lifetime,revoke,g)
				if err!=nil{b.sealed=true;revoke();return streamFault("refused")}
				b.managed=m
			case workerproto.OperationWindowClose:
				if b.managed==nil||b.managed.grant.Metadata!=q{return streamFault("refused")}
				m:=b.managed
				m.mu.Lock()
				phase,failed,active:=m.phase,m.failed,m.active
				m.mu.Unlock()
				if active{return streamFault("busy")}
				if err:=resolver.Settle(c,q,phase,failed);err!=nil{return streamFault("reconcile")}
				b.managed=nil
			}
			out,_:=json.Marshal(workerproto.ManagedMetadataStart{Request:q})
			return out,nil
		case workerproto.OperationMetadataStart,workerproto.OperationStrictTransferStart:
			return b.startManagedRuntime(c,op,body)
		case workerproto.OperationStreamRead,workerproto.OperationStreamFinish,workerproto.OperationStreamStop:
			return b.handleStream(c,op,body)
		default:return streamFault("unsupported")
		}
	}
	session,err:=workerproto.OpenSession(lifetime,r,w,workerproto.OperationCustodian,h)
	if err==nil{<-session.Done();session.Close()}
	cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout);defer cancel()
	return set.SealAndStop(cleanup)
}
