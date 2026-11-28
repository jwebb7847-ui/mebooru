package workerproto

import (
	"context"
	"errors"
	"sync"
)

var ErrDrainPending = errors.New("managed network work is not drained")
var ErrDrainUnconfirmed = errors.New("managed network cleanup is unconfirmed")

// DrainGate is the worker-side admission barrier for future real WAN
// adapters. Unlike NetworkGate, cancellation is NOT completion. Every
// admitted operation retains a lease until it explicitly reports actual
// transport/descendant cleanup. This is a cooperative contract, not OS
// containment. The library must still require the custodian's proof.
type DrainGate struct {
	mu sync.Mutex
	parent context.Context
	online context.Context
	revoke context.CancelFunc
	active int
	draining int
	empty chan struct{}
	sealed bool
	uncertain bool
}

func NewDrainGate(parent context.Context)*DrainGate{
	empty:=make(chan struct{})
	close(empty)
	return &DrainGate{parent:parent,empty:empty}
}

// Open is only called after the library persists online consent. An
// undrained previous generation cannot be reopened, even if canceled.
func(g *DrainGate)Open()error{
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed||g.parent.Err()!=nil{return ErrOffline}
	if g.uncertain{return ErrDrainUnconfirmed}
	if g.online!=nil{return nil}
	if g.active!=0||g.draining!=0{return ErrDrainPending}
	g.online,g.revoke=context.WithCancel(g.parent)
	return nil
}

// Admit requires the exact current generation. Finish is not tied to
// caller cancellation: call it only after every request and owned
// descendant for this lease has terminated. A nonnil cleanup error
// poisons the session permanently, even if other leases finish.
func(g *DrainGate)Admit(caller context.Context)(context.Context,func(error),error){
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.admitLocked(caller)
}

// DrainGeneration is opaque, in-process authorization identity. It is
// never serialized or supplied by a requester. Revocation permanently
// invalidates a generation, including an offline/online cycle.
type DrainGeneration struct{
	owner *DrainGate
	online context.Context
}

func(g *DrainGate)CurrentGeneration()DrainGeneration{
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sealed||g.uncertain||g.online==nil||g.online.Err()!=nil{return DrainGeneration{}}
	return DrainGeneration{owner:g,online:g.online}
}

// AdmitBound checks the captured generation and acquires a lease in
// the same critical section. A stale or foreign gate token cannot admit.
func(g *DrainGate)AdmitBound(caller context.Context,generation DrainGeneration)(context.Context,func(error),error){
	g.mu.Lock()
	defer g.mu.Unlock()
	if generation.owner!=g||generation.online==nil||generation.online!=g.online{return nil,nil,ErrOffline}
	return g.admitLocked(caller)
}

func(g *DrainGate)admitLocked(caller context.Context)(context.Context,func(error),error){
	if g.sealed||g.uncertain||g.online==nil||g.online.Err()!=nil{return nil,nil,ErrOffline}
	if err:=caller.Err();err!=nil{return nil,nil,err}
	if g.active==0{g.empty=make(chan struct{})}
	g.active++
	ctx,cancel:=context.WithCancel(g.online)
	stop:=context.AfterFunc(caller,cancel)
	var once sync.Once
	return ctx,func(cleanupErr error){
		once.Do(func(){
			stop()
			cancel()
			g.mu.Lock()
			defer g.mu.Unlock()
			if cleanupErr!=nil{
				g.uncertain=true
				g.sealed=true
				g.revokeLocked()
			}
			g.active--
			if g.active==0{close(g.empty)}
		})
	},nil
}

func(g *DrainGate)revokeLocked(){
	if g.revoke!=nil{g.revoke();g.revoke=nil;g.online=nil}
}

// Drain closes admission first, then waits for actual completion. A
// timeout is not proof; it leaves admission closed and permits a later
// retry. Concurrent transitions are serialized by the control owner;
// Open is independently refused while a drain is in flight.
func(g *DrainGate)Drain(ctx context.Context)error{return g.drain(ctx,false)}

// Seal permanently closes admission before waiting. Used for quit or
// idle reservation, not a temporary offline transition.
func(g *DrainGate)Seal(ctx context.Context)error{return g.drain(ctx,true)}

func(g *DrainGate)drain(ctx context.Context,seal bool)error{
	g.mu.Lock()
	if seal{g.sealed=true}
	g.revokeLocked()
	empty:=g.empty
	g.draining++
	g.mu.Unlock()
	defer func(){g.mu.Lock();g.draining--;g.mu.Unlock()}()
	select{
	case <-empty:
	case <-ctx.Done():return ctx.Err()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.uncertain{return ErrDrainUnconfirmed}
	if g.active!=0||g.online!=nil{return ErrDrainPending}
	return ctx.Err()
}
