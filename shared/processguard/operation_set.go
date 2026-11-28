package processguard

import (
	"context"
	"errors"
	"sync"
)

var ErrOperationsSealed=errors.New("managed operation ownership sealed")
var ErrOperationLimit=errors.New("managed operation ownership limit")
var ErrOperationUnconfirmed=errors.New("managed operation ownership unconfirmed")

// OperationChild must be dormant on return from a trusted starter.
// Activate is one-shot; Stop must irrevocably revoke activation and
// prove all owned descendants gone, not merely the direct child exited.
type OperationChild interface{
	Activate()error
	Stop(context.Context)error
}

// OperationSet is owned ONLY by the cleanup custodian, not the worker.
// Each independent Unix group or Windows Job must be registered here
// before activation. A successful final receipt requires SealAndStop
// as well as the primary worker containment proof. No IPC or persistence
// is provided here; this is not yet a production launcher.
type OperationSet struct{
	mu sync.Mutex
	children map[*OperationLease]struct{}
	limit int
	sealed bool
	uncertain bool
}

func NewOperationSet(limit int)*OperationSet{
	return &OperationSet{children:make(map[*OperationLease]struct{}),limit:limit}
}

// OperationLease keeps the exact child handle. A failed stop retains it
// for retry. Concurrent stop requests serialize on a context-aware token.
type OperationLease struct{
	set *OperationSet
	child OperationChild
	token chan struct{}
	stopped bool // protected by token
}

// Start serializes dormant launch/registration/activation against sealing.
// A trusted starter must be bounded, cannot reenter this set, and must
// return a nonnil handle on any uncertain startup. Nil child plus error
// guarantees no process was created or remains. Failed activation still
// returns an owned lease; caller must stop it or seal the entire set.
func(s *OperationSet)Start(ctx context.Context,start func(context.Context)(OperationChild,error))(*OperationLease,error){
	return s.startChecked(ctx,start,nil)
}

// startChecked rechecks current trusted authority after dormant launch and
// registration, immediately before activation. check must be bounded and
// cannot reenter the set. This narrows a revocation race, not an atomic
// policy transaction. A refused dormant handle is still owned for Stop.
func(s *OperationSet)startChecked(ctx context.Context,start func(context.Context)(OperationChild,error),check func(context.Context)bool)(*OperationLease,error){
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed||s.uncertain{return nil,ErrOperationsSealed}
	if s.limit<=0||len(s.children)>=s.limit{return nil,ErrOperationLimit}
	if err:=ctx.Err();err!=nil{return nil,err}
	if start==nil{return nil,ErrOperationUnconfirmed}
	child,err:=start(ctx)
	if child==nil{
		if err==nil||errors.Is(err,ErrOperationUnconfirmed){s.uncertain=true;s.sealed=true;return nil,ErrOperationUnconfirmed}
		return nil,err
	}
	l:=&OperationLease{set:s,child:child,token:make(chan struct{},1)}
	l.token<-struct{}{}
	s.children[l]=struct{}{}
	if err!=nil{return l,err}
	if err:=ctx.Err();err!=nil{return l,err}
	if check!=nil&&!check(ctx){return l,ErrOperationsSealed}
	if err:=ctx.Err();err!=nil{return l,err}
	return l,child.Activate()
}

// Stop never releases ownership merely because a caller canceled or the
// child exited. Only the containment implementation can supply proof.
func(l *OperationLease)Stop(ctx context.Context)error{
	select{case <-l.token:case <-ctx.Done():return ctx.Err()}
	defer func(){l.token<-struct{}{}}()
	if l.stopped{return nil}
	if err:=l.child.Stop(ctx);err!=nil{return err}
	l.stopped=true
	l.set.mu.Lock()
	delete(l.set.children,l)
	l.set.mu.Unlock()
	return nil
}

// SealAndStop prevents any new activation, then concurrently revokes and
// verifies every retained operation. No failure is silently discarded.
// A timed-out attempt can be retried; a missing handle contract violation
// can never be converted to successful cleanup in this custodian.
func(s *OperationSet)SealAndStop(ctx context.Context)error{
	s.mu.Lock()
	s.sealed=true
	leases:=make([]*OperationLease,0,len(s.children))
	for l:=range s.children{leases=append(leases,l)}
	uncertain:=s.uncertain
	s.mu.Unlock()
	results:=make(chan error,len(leases))
	for _,l:=range leases{go func(l *OperationLease){results<-l.Stop(ctx)}(l)}
	var errs []error
	if uncertain{errs=append(errs,ErrOperationUnconfirmed)}
	for range leases{
		select{
		case err:=<-results:if err!=nil{errs=append(errs,err)}
		case <-ctx.Done():return errors.Join(append(errs,ctx.Err())...)
		}
	}
	return errors.Join(errs...)
}
