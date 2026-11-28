package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// CommandAuthority is local worker-side authorization supplied by
// the authenticated library session setup. In particular, retry,
// continuation and history removal must reconcile uncertain receipts
// before granting permission. Job execution separately checks current
// owner scope; accepting a queue command is never a WAN grant.
type CommandAuthority func(context.Context,workerproto.Operation,workerproto.QueueCommand)error

// EnqueueManaged persists the exact new job before exposing it to a
// worker or returning acceptance. Failed persistence is ambiguous:
// remove the in-memory pending entry and hold the queue for review.
func(q *Queue) EnqueueManaged(url string,opts Options)(int64,error){
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.managed==nil||q.store==nil||q.closed||q.managedRetiring||q.managedStoreFault{return 0,errStopping}
	if opts.ManagedIntentID!=""{
		id,err:=q.store.ManagedSubmissionJob(opts.ManagedIntentID)
		if err!=nil{return 0,err}
		if id!=0{return 0,errors.New("managed submission already assigned; reconcile original job")}
	}
	j:=q.enqueueLocked(url,opts)
	if err:=q.store.SaveJob(j.Snapshot());err!=nil{
		q.removeFromPendingLocked(j.ID)
		delete(q.index,j.ID)
		q.managedStoreFault=true
		return 0,err
	}
	return j.ID,nil
}

// PrivateDispatch serves actual queue and watch operations through
// the existing private control protocol. Unimplemented operations
// remain explicit refusals, never delegated to HTTP.
func(q *Queue) PrivateDispatch(authorize CommandAuthority,wake func())workerproto.Handler{
	control:=q.ManagedControl(wake)
	var commands sync.Mutex
	return func(ctx context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
		switch op{
		case workerproto.Health,workerproto.SetOffline,workerproto.ReserveIdle,workerproto.Shutdown:
			return control(ctx,op,body)
		}
		fault:=func(code string)(json.RawMessage,*workerproto.Fault){return nil,&workerproto.Fault{Code:code}}
		if q.managed==nil||authorize==nil{return fault("unsupported")}
		cmd,err:=workerproto.DecodeQueueCommand(op,body)
		if err!=nil{return fault("invalid")}
		commands.Lock()
		defer commands.Unlock()
		if ctx.Err()!=nil||authorize(ctx,op,cmd)!=nil{return fault("refused")}
		respond:=func(v any)(json.RawMessage,*workerproto.Fault){
			raw,err:=json.Marshal(v)
			if err!=nil||len(raw)>workerproto.MaxControlBytes/2{return fault("result_limit")}
			return raw,nil
		}
		accepted:=func(id int64,err error)(json.RawMessage,*workerproto.Fault){
			if err!=nil||id<1||q.ManagedStoreError()!=nil{return fault("queue_refused")}
			return respond(struct{JobID int64 `json:"job_id"`}{id})
		}
		done:=func(err error)(json.RawMessage,*workerproto.Fault){
			if err!=nil||q.ManagedStoreError()!=nil{return fault("queue_refused")}
			return respond(struct{OK bool `json:"ok"`}{true})
		}
		switch op{
		case workerproto.QueueList:
			jobs,total:=q.List(ListOptions{Page:cmd.Page,Limit:cmd.Limit,OmitItems:true})
			return respond(struct{Jobs []*Job `json:"jobs"`;Total int `json:"total"`}{jobs,total})
		case workerproto.QueueGet:
			j,err:=q.Get(cmd.JobID);if err!=nil{return fault("not_found")}
			return respond(j)
		case workerproto.QueueEnqueue:
			id,err:=q.EnqueueManaged(cmd.URL,Options{Gallery:cmd.Gallery,Site:workerproto.StrictQueueSource(cmd.URL),MaxItems:cmd.MaxItems,ManagedIntentID:cmd.IntentID})
			return accepted(id,err)
		case workerproto.QueueRetry:
			return done(q.Retry(cmd.JobID,cmd.Force))
		case workerproto.QueueContinue:
			id,err:=q.Continue(cmd.JobID);return accepted(id,err)
		case workerproto.QueueContinueAll:
			id,err:=q.ContinueAll(cmd.JobID);return accepted(id,err)
		case workerproto.QueueCancel:
			return done(q.CancelLive(cmd.JobID))
		case workerproto.QueueRemoveSeries:
			return done(q.CancelSeries(cmd.JobID))
		case workerproto.QueuePause:
			q.Pause();return done(nil)
		case workerproto.QueueResume:
			q.Resume();return done(nil)
		case workerproto.QueueClear:
			q.Clear();return done(nil)
		case workerproto.QueueClearSucceeded:
			q.ClearSucceeded();return done(nil)
		case workerproto.QueueCancelPending:
			q.CancelPending();return done(nil)
		case workerproto.WatchList:
			return respond(q.Watches())
		case workerproto.WatchAdd:
			// Watches retain the existing default-gallery model. The
			// owner must explicitly authorize that model at submission
			// and again when the due job is resolved.
			id,err:=q.AddWatch(cmd.URL,cmd.IntervalHours,cmd.MaxItems,nil)
			if err!=nil||id<1||q.ManagedStoreError()!=nil{return fault("queue_refused")}
			return respond(struct{WatchID int64 `json:"watch_id"`}{id})
		case workerproto.WatchSetInterval:
			return done(q.SetWatchInterval(cmd.WatchID,cmd.IntervalHours))
		case workerproto.WatchSetItems:
			return done(q.SetWatchItems(cmd.WatchID,cmd.MaxItems))
		case workerproto.WatchCheck:
			id,err:=q.CheckWatch(cmd.WatchID,false);return accepted(id,err)
		case workerproto.WatchRemove:
			return done(q.RemoveWatch(cmd.WatchID))
		default:return fault("unsupported")
		}
	}
}

// ManagedStoreError exposes the durable admission failure. A normal
// resume cannot clear it; reopen/reconcile persisted state instead.
func(q *Queue) ManagedStoreError()error{
	q.mu.Lock();defer q.mu.Unlock()
	if q.managedStoreFault{return errors.New("managed queue persistence requires reconciliation")}
	return nil
}
