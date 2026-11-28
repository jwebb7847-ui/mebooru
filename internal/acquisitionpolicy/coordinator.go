package acquisitionpolicy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Peer is implemented by the future owned-worker supervisor, not by the
// browser or an external plugin. ApplyOffline(true) MUST acknowledge only
// after closing admissions and draining network work, including descendants.
// Stop must reap/contain the worker and report errors truthfully. Until these
// obligations are implemented this coordinator is not runtime enforcement.
type Peer interface {
	// Done closes when the peer/session can no longer accept work.
	Done() <-chan struct{}
	ApplyOffline(context.Context, bool) error
	Stop(context.Context) error
}

type policyStore interface {
	Load() (bool, error)
	Save(bool) error
}

var ErrPeerAttached = errors.New("previous acquisition worker has not been released")
var ErrPeerUnusable = errors.New("acquisition worker must be released before authorizing work")

const cleanupTimeout = 10 * time.Second

type onlineLease struct { done <-chan struct{}; revoked chan struct{} }

// Coordinator serializes policy changes and worker attachment. It is owned by
// the library supervisor, with one instance per exclusively locked profile.
// EffectiveOnline is an admission hint, not a guarantee of network activity.
// Worker-side gates remain authoritative for execution.
type Coordinator struct {
	mu sync.Mutex
	store policyStore
	offline bool
	peer Peer
	peerDone <-chan struct{}
	poisoned bool
	online atomic.Pointer[onlineLease]
}

// New loads the persisted setting. On a damaged/unreadable record the
// returned coordinator is still usable offline; surface the error and do
// not discard the offline state or infer consent from legacy configuration.
func New(store *Store) (*Coordinator, error) { return newCoordinator(store) }

func newCoordinator(store policyStore) (*Coordinator, error) {
	offline, err := store.Load()
	return &Coordinator{store:store,offline:offline || err!=nil},err
}

func (c *Coordinator) EffectiveOnline() bool {
	lease:=c.online.Load()
	if lease==nil{return false}
	select{
	case <-lease.done:return false
	default:return true
	}
}

// Caller holds mu.
func (c *Coordinator) unusable() bool {
	if c.poisoned{return true}
	select{
	case <-c.peerDone:return true
	default:return false
	}
}

func (c *Coordinator) acknowledge(ctx context.Context) error {
	if err:=ctx.Err();err!=nil{return err}
	if c.unusable(){return ErrPeerUnusable}
	if !c.offline{c.online.Store(&onlineLease{done:c.peerDone,revoked:make(chan struct{})})}
	return nil
}

// ConfiguredOffline is the current desired policy, including fail-closed
// in-memory state after a failed write. A failed transition is not saved
// consent; callers must display its error rather than assert persistence.
func (c *Coordinator) ConfiguredOffline() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offline
}

// Attach requires a newly handshaken, default-deny managed peer. A failed
// application of policy triggers cleanup, never fallback to legacy HTTP.
func (c *Coordinator) Attach(ctx context.Context, peer Peer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peer!=nil {return ErrPeerAttached}
	if peer==nil {return errors.New("missing managed acquisition worker")}
	c.revokeOnline()
	c.peer=peer
	c.peerDone=peer.Done()
	if c.peerDone==nil{return c.stopOnFailure(ctx,ErrPeerUnusable)}
	if c.unusable(){return c.stopOnFailure(ctx,ErrPeerUnusable)}
	if err:=peer.ApplyOffline(ctx,c.offline);err!=nil{
		return c.stopOnFailure(ctx,err)
	}
	if err:=c.acknowledge(ctx);err!=nil{return c.stopOnFailure(ctx,err)}
	return nil
}

// SetOffline closes library admissions before changing the setting. Persist
// the requested policy before reporting success or granting any new online
// permission. On failure deny admissions and stop the current peer; a disk
// failure may leave the old persisted record, and is returned to the caller.
func (c *Coordinator) SetOffline(ctx context.Context, offline bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revokeOnline()
	if offline {c.offline=true}
	if !offline && c.peer!=nil && c.unusable(){return ErrPeerUnusable}
	if err:=c.store.Save(offline);err!=nil{
		c.offline=true
		return c.stopOnFailure(ctx,err)
	}
	c.offline=offline
	if c.peer==nil{return nil}
	if c.unusable(){return c.stopOnFailure(ctx,ErrPeerUnusable)}
	if err:=c.peer.ApplyOffline(ctx,offline);err!=nil{
		return c.stopOnFailure(ctx,err)
	}
	if err:=c.acknowledge(ctx);err!=nil{return c.stopOnFailure(ctx,err)}
	return nil
}

// Release stops the current peer before clearing its ownership. Keep the
// reference on cleanup failure so a second worker cannot silently attach.
// Done invalidates admission on peer loss; the supervisor must still reap.
func (c *Coordinator) Release(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revokeOnline()
	if c.peer==nil{return nil}
	c.poisoned=true
	if err:=c.peer.Stop(ctx);err!=nil{return err}
	c.clearPeer()
	return nil
}

// Caller holds mu. Cleanup has an independent bounded context so a canceled
// UI request does not automatically suppress worker shutdown.
func (c *Coordinator) stopOnFailure(ctx context.Context, cause error) error {
	c.revokeOnline()
	if c.peer==nil{return cause}
	c.poisoned=true
	cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),cleanupTimeout)
	defer cancel()
	if err:=c.peer.Stop(cleanup);err!=nil{return errors.Join(cause,err)}
	c.clearPeer()
	return cause
}

func (c *Coordinator) clearPeer(){
	c.peer=nil
	c.peerDone=nil
	c.poisoned=false
}

// Called under the coordinator lock; old generations never revive.
func(c *Coordinator)revokeOnline(){
	if old:=c.online.Swap(nil);old!=nil{close(old.revoked)}
}
