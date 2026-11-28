package queue

import(
	"context"
	"encoding/json"
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
	return NewManagedWithGate(workerproto.NewDrainGate(parent),proc,workers,maxFinished)
}

// NewManagedWithGate permits the actual contained processor and PTR
// client to share the exact same worker-side admission gate.
func NewManagedWithGate(gate *workerproto.DrainGate,proc ManagedProcessor,workers,maxFinished int)(*Queue,error){
	if gate==nil||proc==nil||gate.CurrentGeneration()!=(workerproto.DrainGeneration{}){return nil,errors.New("managed queue requires a fresh closed gate")}
	q:=New(nil,workers,maxFinished)
	q.managed=&managedQueueState{gate:gate,proc:proc}
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
	if q.closed||q.managedRetiring||q.managedStoreFault{q.mu.Unlock();return errors.Join(errStopping,m.gate.Drain(ctx))}
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
	if q.closed||q.managedRetiring||q.managedStoreFault||len(q.pending)!=0||len(q.running)!=0{q.mu.Unlock();return false,nil}
	if !q.managedOffline&&q.managedWatchesEnabled{
		for _,watch:=range q.watches{if watch.IntervalHours>0&&!q.now().Before(watch.Next()){q.mu.Unlock();return false,nil}}
	}
	q.managedRetiring=true
	q.managedOffline=true
	q.mu.Unlock()
	if err:=m.gate.Seal(ctx);err!=nil{return false,err}
	return true,nil
}


// CloseManaged first closes and drains every shared admission (queue,
// runtime, PTR transport), then joins the actual queue workers.
func(q *Queue) CloseManaged(ctx context.Context)error{
	if q==nil||q.managed==nil{return errors.New("not a managed queue")}
	m:=q.managed
	m.mu.Lock();defer m.mu.Unlock()
	q.Stop()
	if err:=m.gate.Seal(ctx);err!=nil{return err}
	q.Close()
	return nil
}

// ManagedControl is the real worker control handler, not the diagnostic
// probe. Use only on a private inherited session; unsupported operations
// refuse rather than route to legacy HTTP. A trusted owner may provide
// a local wake callback for already-enabled PTR or explicit schedules.
func(q *Queue) ManagedControl(wake func())workerproto.Handler{
	return func(ctx context.Context,op workerproto.Operation,payload json.RawMessage)(json.RawMessage,*workerproto.Fault){
		bad:=func()(json.RawMessage,*workerproto.Fault){return nil,&workerproto.Fault{Code:"invalid"}}
		fail:=func()(json.RawMessage,*workerproto.Fault){return nil,&workerproto.Fault{Code:"cleanup_unconfirmed"}}
		if q==nil||q.managed==nil{return nil,&workerproto.Fault{Code:"unsupported"}}
		switch op{
		case workerproto.Health:
			if len(payload)!=0{return bad()}
			q.mu.Lock()
			v:=struct{Ready bool `json:"ready"`;Offline bool `json:"offline"`;Operations int `json:"operations"`}{!q.closed,q.managedOffline,len(q.running)}
			q.mu.Unlock()
			body,_:=json.Marshal(v);return body,nil
		case workerproto.SetOffline:
			p,err:=workerproto.DecodeOfflinePolicy(payload);if err!=nil{return bad()}
			if err:=q.SetManagedOffline(ctx,p.Offline);err!=nil{return fail()}
			if !p.Offline&&wake!=nil{wake()}
			body,_:=json.Marshal(workerproto.OfflineAck{Offline:p.Offline,Drained:p.Offline});return body,nil
		case workerproto.ReserveIdle:
			if len(payload)!=0{return bad()}
			idle,err:=q.ReserveManagedIdle(ctx);if err!=nil{return fail()}
			body,_:=json.Marshal(workerproto.IdleReservation{Reserved:idle});return body,nil
		case workerproto.Shutdown:
			if len(payload)!=0{return bad()}
			if err:=q.CloseManaged(ctx);err!=nil{return fail()}
			return nil,nil
		default:return nil,&workerproto.Fault{Code:"unsupported"}
		}
	}
}

// EnableManagedWatches must be set only by explicit owner configuration.
// The existing RunWatches loop then checks this opt-in and the online gate.
func(q *Queue)EnableManagedWatches(enabled bool){
	q.mu.Lock();defer q.mu.Unlock()
	if q.managed!=nil&&!q.managedRetiring&&!q.closed{q.managedWatchesEnabled=enabled}
}
