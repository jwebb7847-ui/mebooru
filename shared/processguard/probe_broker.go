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

// probeBroker is a one-session diagnostic adapter. Its starter is a
// fixed trusted fixture supplied by the custodian, not by a request.
// The actual custodian must also retain the same OperationSet for final
// receipt proof. No path, argv, environment, output or network API.
type probeBroker struct{
	mu sync.Mutex
	set *OperationSet
	start ProbeStarter
	next uint64
	leases map[uint64]*OperationLease
	sealed bool
}
func(b *probeBroker)handle(ctx context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
	fault:=func(code string)(json.RawMessage,*workerproto.Fault){return nil,&workerproto.Fault{Code:code}}
	switch op{
	case workerproto.OperationProbeStart:
		if len(body)!=0{return fault("invalid")}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.sealed||b.next==^uint64(0){return fault("closed")}
		if err:=ctx.Err();err!=nil{return fault("canceled")}
		lease,err:=b.set.Start(ctx,b.start)
		if err!=nil||ctx.Err()!=nil{
			if lease!=nil{
				cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
				stopErr:=lease.Stop(cleanup)
				done()
				if stopErr!=nil{b.sealed=true;return fault("cleanup_unconfirmed")}
			}
			if errors.Is(err,ErrOperationLimit){return fault("busy")}
			if errors.Is(err,ErrOperationUnconfirmed){b.sealed=true;return fault("cleanup_unconfirmed")}
			return fault("start_failed")
		}
		b.next++
		b.leases[b.next]=lease
		out,_:=json.Marshal(workerproto.ProbeOperationRef{ID:b.next})
		return out,nil
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
			b.mu.Lock();b.sealed=true;b.mu.Unlock()
			return fault("cleanup_unconfirmed")
		}
		b.mu.Lock();delete(b.leases,ref.ID);b.mu.Unlock()
		out,_:=json.Marshal(ref)
		return out,nil
	default:return fault("unsupported")
	}
}

// ServeProbeBroker owns the separate inherited control channel for
// one worker generation. On EOF, handshake failure, cancellation or
// protocol failure, seal and stop every operation. Never reuse this set
// or channel for another worker. A nil return proves set cleanup only,
// not main worker termination. The caller must combine both proofs.
func ServeProbeBroker(ctx context.Context,r io.ReadCloser,w io.WriteCloser,set *OperationSet,start ProbeStarter)error{
	if set==nil||start==nil{
		_ = r.Close();_ = w.Close()
		return ErrOperationUnconfirmed
	}
	b:=&probeBroker{set:set,start:start,leases:make(map[uint64]*OperationLease)}
	s,err:=workerproto.OpenSession(ctx,r,w,workerproto.OperationCustodian,b.handle)
	if err==nil{<-s.Done();s.Close()}
	// Seal the underlying set first: a handler already waiting for its
	// mutex cannot activate after the final stop snapshot.
	cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
	defer done()
	return set.SealAndStop(cleanup)
}
