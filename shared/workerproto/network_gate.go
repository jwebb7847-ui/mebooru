package workerproto

import (
	"context"
	"errors"
	"sync"
)

var ErrOffline = errors.New("private worker network permission is closed")

// NetworkGate is a cooperative admission and cancellation primitive.
// It starts offline and has no filesystem or network hooks. The future
// worker must route EVERY WAN path, including descendants, through policy
// enforcement; closing this gate is not proof that network activity stopped.
type NetworkGate struct {
	mu sync.Mutex
	parent context.Context
	online context.Context
	revoke context.CancelFunc
	closed bool
}

func NewNetworkGate(parent context.Context) *NetworkGate {
	return &NetworkGate{parent:parent}
}

// SetOffline revokes the current generation on a transition to offline.
// An old generation never becomes valid again. Repeated online updates
// do not replace the current generation. The owner must persist policy
// before opening the gate; this type does not persist configuration.
func (g *NetworkGate) SetOffline(offline bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.parent.Err()!=nil {
		return ErrOffline
	}
	if offline {
		if g.revoke!=nil {
			g.revoke()
			g.online=nil
			g.revoke=nil
		}
		return nil
	}
	if g.online==nil {
		g.online,g.revoke=context.WithCancel(g.parent)
	}
	return nil
}

// Admit derives an operation context from the currently authorized
// generation and the caller. The caller must cancel when finished.
// A returned context can be revoked immediately, so callers must check it
// before side effects and pass it through every cancellable operation.
func (g *NetworkGate) Admit(caller context.Context) (context.Context, context.CancelFunc, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.online==nil || g.online.Err()!=nil {
		return nil,nil,ErrOffline
	}
	if err:=caller.Err();err!=nil{return nil,nil,err}
	ctx,cancel:=context.WithCancel(g.online)
	stop:=context.AfterFunc(caller,cancel)
	return ctx,func(){stop();cancel()},nil
}

// Close is permanent for this worker session. Reopening requires a new
// gate/session and fresh authorization from the supervisor.
func (g *NetworkGate) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed=true
	if g.revoke!=nil{
		g.revoke()
		g.online=nil
		g.revoke=nil
	}
}
