package queue

import(
	"context"
	"errors"
	"sync"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

var errManagedCleanup=errors.New("managed queue cleanup unconfirmed")

// ManagedProcessor is a local, contained processor, never the legacy
// HTTP/WAN processor. The second result is actual cleanup uncertainty;
// a canceled request alone cannot be reported as cleaned. It must
// support only explicitly authorized job kinds and durable outcomes.
type ManagedProcessor interface{
	ProcessManaged(context.Context,*Job)(error,error)
}
type managedQueueState struct{
	mu sync.Mutex
	gate *workerproto.DrainGate
	proc ManagedProcessor
}

// NewManaged uses the existing durable queue, watch and continuation
// machinery with independent default-deny network admission. There is
// no legacy Processor to fall back to. Attach Store before Start.
func NewManaged(parent context.Context,proc ManagedProcessor,workers,maxFinished int)(*Queue,error){
	if parent==nil||proc==nil||parent.Err()!=nil{return nil,errors.New("managed queue unavailable")}
	q:=New(nil,workers,maxFinished)
	q.managed=&managedQueueState{gate:workerproto.NewDrainGate(parent),proc:proc}
	q.managedOffline=true
	return q,nil
}
func(q *Queue) ManagedGate()*workerproto.DrainGate{
	if q==nil||q.managed==nil{return nil}
	return q.managed.gate
}

// SetManagedOffline is only called by the private library policy owner.
// Persisting the setting precedes this worker-side call. Pending jobs
// remain queued; active jobs receive cancellation and settle interrupted.
func(q *Queue) SetManagedOffline(ctx context.Context,offline bool)error{
	if q==nil||q.managed==nil{return errors.New("not a managed queue")}
	m:=q.managed
	m.mu.Lock();defer m.mu.Unlock()
	q.mu.Lock()
	if q.closed||q.managedRetiring{q.mu.Unlock();return errStopping}
	q.managedOffline=true
	q.mu.Unlock()
	if offline{return m.gate.Drain(ctx)}
	if ctx.Err()!=nil{return ctx.Err()}
	if err:=m.gate.Open();err!=nil{return err}
	q.mu.Lock()
	q.managedOffline=false
	q.cond.Broadcast()
	q.mu.Unlock()
	return nil
}

// ReserveManagedIdle holds the real queue admission lock. No pending
// or running jobs may exist; future enqueues/retries/continuations
// refuse after this reservation. Watches remain persisted and due.
func(q *Queue) ReserveManagedIdle(ctx context.Context)(bool,error){
	if q==nil||q.managed==nil{return false,errors.New("not a managed queue")}
	m:=q.managed
	m.mu.Lock();defer m.mu.Unlock()
	q.mu.Lock()
	if q.closed||q.managedRetiring||len(q.pending)!=0||len(q.running)!=0{q.mu.Unlock();return false,nil}
	q.managedRetiring=true
	q.managedOffline=true
	q.mu.Unlock()
	if err:=m.gate.Seal(ctx);err!=nil{return false,err}
	return true,nil
}
