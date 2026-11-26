package processguard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

const probeCleanupTimeout=10*time.Second
type ProbeStarter func(context.Context)(OperationChild,error)

// VersionProbeChild is a trusted dormant local diagnostic runtime.
// Version reads its bounded result; Stop still owns actual cleanup.
type VersionProbeChild interface{
	OperationChild
	Version(context.Context)(string,error)
}
type VersionProbeStarter func(context.Context)(VersionProbeChild,error)

// probeBroker is a one-session diagnostic adapter. Its starter is a
// fixed trusted fixture supplied by the custodian, not by a request.
// The actual custodian must also retain the same OperationSet for final
// receipt proof. No path, argv, environment, output or network API.
type probeBroker struct{
	mu sync.Mutex
	set *OperationSet
	start ProbeStarter
	transfer ProbeStarter
	version VersionProbeStarter
	stream StreamProbeStarter
	streams map[uint64]*streamProbe
	next uint64
	leases map[uint64]*OperationLease
	sealed bool
	revoke context.CancelFunc
}
func(b *probeBroker)handle(ctx context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
	fault:=func(code string)(json.RawMessage,*workerproto.Fault){return nil,&workerproto.Fault{Code:code}}
	switch op{
	case workerproto.OperationProbeStart,workerproto.OperationTransferProbeStart:
		if len(body)!=0{return fault("invalid")}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.sealed||b.next==^uint64(0){return fault("closed")}
		if err:=ctx.Err();err!=nil{return fault("canceled")}
		starter:=b.start
		if op==workerproto.OperationTransferProbeStart{
			starter=b.transfer
			if starter==nil{return fault("unsupported")}
		}
		lease,err:=b.set.Start(ctx,starter)
		if err!=nil||ctx.Err()!=nil{
			if lease!=nil{
				cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
				stopErr:=lease.Stop(cleanup)
				done()
				if stopErr!=nil{b.sealed=true;b.revoke();return fault("cleanup_unconfirmed")}
			}
			// Only a trusted no-child refusal can be acknowledged as
			// offline without invalidating the broker session.
			if op==workerproto.OperationTransferProbeStart&&lease==nil&&errors.Is(err,workerproto.ErrOffline){return fault("offline")}
			if errors.Is(err,ErrOperationLimit){return fault("busy")}
			if errors.Is(err,ErrOperationUnconfirmed){b.sealed=true;b.revoke();return fault("cleanup_unconfirmed")}
			return fault("start_failed")
		}
		b.next++
		b.leases[b.next]=lease
		out,_:=json.Marshal(workerproto.ProbeOperationRef{ID:b.next})
		return out,nil
	case workerproto.OperationStreamStart,workerproto.OperationStreamRead,workerproto.OperationStreamFinish,workerproto.OperationStreamStop:
		return b.handleStream(ctx,op,body)
	case workerproto.OperationVersionProbe:
		if len(body)!=0{return fault("invalid")}
		if b.version==nil{return fault("unsupported")}
		return b.runVersion(ctx)
	case workerproto.OperationProbeStop:
		ref,err:=workerproto.DecodeProbeOperationRef(body)
		if err!=nil{return fault("invalid")}
		b.mu.Lock()
		lease:=b.leases[ref.ID]
		b.mu.Unlock()
		if lease==nil{return fault("unknown_operation")}
		// Once requested, cleanup continues independently of the RPC
		// deadline. A successful response means actual containment proof.
		cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
		defer done()
		if err:=lease.Stop(cleanup);err!=nil{
			b.mu.Lock();b.sealed=true;b.revoke();b.mu.Unlock()
			return fault("cleanup_unconfirmed")
		}
		b.mu.Lock();delete(b.leases,ref.ID);b.mu.Unlock()
		out,_:=json.Marshal(ref)
		return out,nil
	default:return fault("unsupported")
	}
}

// No success can escape until the registered lease proves cleanup.
// Failed launch with a handle also requires cleanup; no request deadline
// can waive that requirement. Other owned operations remain independent.
func(b *probeBroker)runVersion(ctx context.Context)(json.RawMessage,*workerproto.Fault){
	ctx,deadline:=context.WithTimeout(ctx,15*time.Second)
	defer deadline()
	fault:=func(code string)(json.RawMessage,*workerproto.Fault){return nil,&workerproto.Fault{Code:code}}
	b.mu.Lock()
	if b.sealed{b.mu.Unlock();return fault("closed")}
	var child VersionProbeChild
	lease,startErr:=b.set.Start(ctx,func(start context.Context)(OperationChild,error){
		var err error
		child,err=b.version(start)
		if child==nil{return nil,err}
		return child,err
	})
	b.mu.Unlock()
	var version string
	readErr:=startErr
	if readErr==nil&&lease!=nil{
		version,readErr=child.Version(ctx)
	}
	if lease!=nil{
		cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
		stopErr:=lease.Stop(cleanup)
		done()
		if stopErr!=nil{
			b.mu.Lock();b.sealed=true;b.revoke();b.mu.Unlock()
			return fault("cleanup_unconfirmed")
		}
	}
	if errors.Is(startErr,ErrOperationUnconfirmed){
		b.mu.Lock();b.sealed=true;b.revoke();b.mu.Unlock()
		return fault("cleanup_unconfirmed")
	}
	if ctx.Err()!=nil{return fault("canceled")}
	if readErr!=nil||lease==nil{return fault("runtime_probe_failed")}
	out,_:=json.Marshal(workerproto.VersionProbeResult{Version:version})
	if _,err:=workerproto.DecodeVersionProbeResult(out);err!=nil{return fault("runtime_probe_failed")}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sealed{return fault("closed")}
	return out,nil
}

// ServeProbeBroker owns the separate inherited control channel for
// one worker generation. On EOF, handshake failure, cancellation or
// protocol failure, seal and stop every operation. Never reuse this set
// or channel for another worker. A nil return proves set cleanup only,
// not main worker termination. The caller must combine both proofs.
func ServeProbeBroker(ctx context.Context,r io.ReadCloser,w io.WriteCloser,set *OperationSet,start ProbeStarter)error{
	return ServeProbeBrokerWithVersion(ctx,r,w,set,start,nil)
}

// Optional fixed local runtime starter is provided by trusted custody
// startup only. It is never configured by the requester.
func ServeProbeBrokerWithVersion(ctx context.Context,r io.ReadCloser,w io.WriteCloser,set *OperationSet,start ProbeStarter,version VersionProbeStarter)error{
	return ServeProbeBrokerWithStream(ctx,r,w,set,start,version,nil)
}
func ServeProbeBrokerWithStream(ctx context.Context,r io.ReadCloser,w io.WriteCloser,set *OperationSet,start ProbeStarter,version VersionProbeStarter,stream StreamProbeStarter)error{
	return ServeProbeBrokerWithTransfer(ctx,r,w,set,start,version,stream,nil)
}
func ServeProbeBrokerWithTransfer(ctx context.Context,r io.ReadCloser,w io.WriteCloser,set *OperationSet,start ProbeStarter,version VersionProbeStarter,stream StreamProbeStarter,transfer ProbeStarter)error{
	if set==nil||start==nil{
		_ = r.Close();_ = w.Close()
		return ErrOperationUnconfirmed
	}
	lifetime,revoke:=context.WithCancel(ctx)
	defer revoke()
	b:=&probeBroker{set:set,start:start,transfer:transfer,version:version,stream:stream,streams:make(map[uint64]*streamProbe),leases:make(map[uint64]*OperationLease),revoke:revoke}
	s,err:=workerproto.OpenSession(lifetime,r,w,workerproto.OperationCustodian,b.handle)
	if err==nil{<-s.Done();s.Close()}
	// Seal the underlying set first: a handler already waiting for its
	// mutex cannot activate after the final stop snapshot.
	cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
	defer done()
	return set.SealAndStop(cleanup)
}
