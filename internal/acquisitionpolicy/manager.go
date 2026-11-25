package acquisitionpolicy

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrLifecycleClosed = errors.New("managed acquisition lifecycle is closed")
	ErrRestartBackoff = errors.New("managed acquisition restart cooling down")
	ErrActiveLeases = errors.New("previous worker still has active operation leases")
)

// ManagedPeer extends policy acknowledgement with an atomic idle-stop
// reservation. ReserveIdle must return true only after atomically refusing
// new jobs/watches/sync and verifying durable state is safely idle. Stop
// then drains and proves owned descendants stopped. False leaves the
// peer usable. These are adapter obligations, not implemented here.
type ManagedPeer interface {
	Peer
	ReserveIdle(context.Context) (bool,error)
}

// Starter receives the application lifetime separately from the bounded
// request/startup context. A nil peer on error MUST mean there is no
// owned child left to clean up. If cleanup is unconfirmed, return the
// peer and error together so replacement remains blocked.
// The starter may prepare only a dormant custodian before fence.Arm.
// It must arm successfully before activating a worker. The custodian
// independently checks the armed generation before actual launch.
type Starter func(startup,lifetime context.Context,fence *WorkerFence)(ManagedPeer,error)

type LifecycleOptions struct {
	IdleAfter time.Duration
	RestartDelay time.Duration
}

// Manager owns one profile lock and exclusively owns its Coordinator.
// All lifecycle operations are serialized. It does not spawn background
// goroutines, replay failed mutations or create a scheduler: the library
// calls Tick for durable scheduled demand while open. Never call back into
// Manager from a Starter or ManagedPeer method.
type Manager struct {
	mu sync.Mutex
	lifetime context.Context
	lock *ProfileLock
	policy *Coordinator
	start Starter
	opts LifecycleOptions
	now func()time.Time

	peer ManagedPeer
	fence *WorkerFence
	unconfirmedStart bool
	attached bool
	invalid bool
	active int
	lastUse time.Time
	retryAfter time.Time
	closing bool
	closed bool
}

// NewManager transfers ownership of lock and policy to Manager on
// success. The caller must hold the lock and must not close/reuse it or
// independently use the Coordinator while the manager owns it.
func NewManager(lifetime context.Context,lock *ProfileLock,policy *Coordinator,start Starter,opts LifecycleOptions)(*Manager,error){
	if lifetime==nil||lock==nil||policy==nil||start==nil{return nil,errors.New("incomplete managed lifecycle configuration")}
	if err:=lock.checkRecovery();err!=nil{return nil,err}
	if opts.IdleAfter<=0{opts.IdleAfter=2*time.Minute}
	if opts.RestartDelay<=0{opts.RestartDelay=2*time.Second}
	return &Manager{lifetime:lifetime,lock:lock,policy:policy,start:start,opts:opts,now:time.Now},nil
}

// Lease keeps the worker from idling out while an operation is active.
// Release is idempotent. Consumers must also observe Peer.Done and never
// replay an uncertain mutation after loss without operation-level reconciliation.
type Lease struct {
	Peer ManagedPeer
	release func()
	once sync.Once
}
func(l *Lease)Release(){l.once.Do(l.release)}

func(m *Manager)Acquire(ctx context.Context)(*Lease,error){
	m.mu.Lock()
	defer m.mu.Unlock()
	if err:=m.ensure(ctx);err!=nil{return nil,err}
	m.active++
	return &Lease{Peer:m.peer,release:func(){
		m.mu.Lock()
		defer m.mu.Unlock()
		m.active--
		m.lastUse=m.now()
	}},nil
}

func(m *Manager)available(ctx context.Context)error{
	if m.closing||m.closed||m.lifetime.Err()!=nil{return ErrLifecycleClosed}
	return ctx.Err()
}

func(m *Manager)peerLost()bool{
	if m.peer==nil{return false}
	if m.invalid||m.peer.Done()==nil{return true}
	select{case <-m.peer.Done():return true;default:return false}
}

// Caller holds mu. No automatic replay or endless restart loop.
func(m *Manager)ensure(ctx context.Context)error{
	if err:=m.available(ctx);err!=nil{return err}
	if m.peer!=nil{
		if !m.peerLost(){return nil}
		if m.active>0{return ErrActiveLeases}
		m.retryAfter=m.now().Add(m.opts.RestartDelay)
		if err:=m.stopPeer(ctx);err!=nil{return err}
	}
	// A stopped peer may still have an unpersisted clean marker. Retry
	// only the same proof; never spawn another worker until it is saved.
	if m.fence!=nil{if err:=m.stopPeer(ctx);err!=nil{return err}}
	if m.now().Before(m.retryAfter){return ErrRestartBackoff}
	fence,err:=m.lock.PrepareCustody()
	if err!=nil{return err}
	m.fence=fence
	peer,err:=m.start(ctx,m.lifetime,fence)
	if peer!=nil&&!fence.isArmed(){err=errors.Join(err,ErrCustodyActivation)}
	m.peer=peer
	if err!=nil||peer==nil{
		if err==nil{
			// A nil peer with no error violates the starter contract. Do not
			// infer that no child exists, or release the ownership fence.
			m.unconfirmedStart=true
			err=errors.New("starter returned no worker")
		}
		m.retryAfter=m.now().Add(m.opts.RestartDelay)
		if peer!=nil{
			m.invalid=true
			cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),cleanupTimeout)
			defer cancel()
			err=errors.Join(err,m.stopPeer(cleanup))
		}else if !m.unconfirmedStart{
			// Starter explicitly guarantees nil peer on error means no
			// owned processes remain. Preserve the marker on sync failure.
			err=errors.Join(err,m.finishFence())
		}
		return err
	}
	m.attached=true
	if err:=m.policy.Attach(ctx,peer);err!=nil{
		m.invalid=true
		m.retryAfter=m.now().Add(m.opts.RestartDelay)
		// Attach may already have cleaned up; Release is idempotent.
		cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),cleanupTimeout)
		defer cancel()
		return errors.Join(err,m.stopPeer(cleanup))
	}
	m.lastUse=m.now()
	return nil
}

// Tick consumes demand from the library's durable scheduler. It must not
// be called for future network-only watches while offline. Demand can be
// local work. No demand and an idle interval only request a reservation;
// the worker decides atomically whether its actual workload is safe to stop.
func(m *Manager)Tick(ctx context.Context,demand bool)error{
	m.mu.Lock()
	defer m.mu.Unlock()
	if err:=m.available(ctx);err!=nil{return err}
	if demand{
		if err:=m.ensure(ctx);err!=nil{return err}
		m.lastUse=m.now()
		return nil
	}
	if m.peer==nil{return nil}
	if m.peerLost(){
		if m.active>0{return ErrActiveLeases}
		if err:=m.stopPeer(ctx);err!=nil{return err}
		m.retryAfter=m.now().Add(m.opts.RestartDelay)
		return nil
	}
	if m.active>0||m.now().Sub(m.lastUse)<m.opts.IdleAfter{return nil}
	idle,err:=m.peer.ReserveIdle(ctx)
	if err!=nil{
		m.invalid=true
		m.retryAfter=m.now().Add(m.opts.RestartDelay)
		cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),cleanupTimeout)
		defer cancel()
		return errors.Join(err,m.stopPeer(cleanup))
	}
	if !idle{
		m.lastUse=m.now()
		return nil
	}
	m.invalid=true
	return m.stopPeer(ctx)
}

func(m *Manager)SetOffline(ctx context.Context,offline bool)error{
	m.mu.Lock()
	defer m.mu.Unlock()
	if err:=m.available(ctx);err!=nil{return err}
	// A startup failure with unconfirmed cleanup may own a peer not yet
	// attached to policy. Never grant online until that peer is released.
	if m.unconfirmedStart{
		if !offline{return ErrWorkerRecoveryRequired}
		return errors.Join(m.policy.SetOffline(ctx,true),ErrWorkerRecoveryRequired)
	}
	if !offline&&m.peer!=nil&&m.peerLost(){return ErrPeerUnusable}
	err:=m.policy.SetOffline(ctx,offline)
	if err!=nil&&m.peer!=nil{m.invalid=true}
	if offline&&m.peer!=nil&&!m.attached{
		cleanup,cancel:=context.WithTimeout(context.WithoutCancel(ctx),cleanupTimeout)
		defer cancel()
		return errors.Join(err,m.stopPeer(cleanup))
	}
	return err
}

func(m *Manager)EffectiveOnline()bool{return m.policy.EffectiveOnline()}

// Caller holds mu. Failure retains ownership and prevents replacement.
func(m *Manager)stopPeer(ctx context.Context)error{
	if m.unconfirmedStart{return ErrWorkerRecoveryRequired}
	if m.peer==nil{return m.finishFence()}
	m.invalid=true
	var err error
	if m.attached{err=m.policy.Release(ctx)}else{err=m.peer.Stop(ctx)}
	if err!=nil{return err}
	m.peer=nil
	m.attached=false
	m.invalid=false
	return m.finishFence()
}

// finishFence may run only after the starter or Stop contract established
// that no owned processes remain. A failed sync retains the capability.
func(m *Manager)finishFence()error{
	if m.fence==nil{return nil}
	if err:=m.fence.ConfirmStopped();err!=nil{return err}
	m.fence=nil
	return nil
}

// Shutdown permanently refuses new work and releases the profile lock
// only after confirmed worker cleanup. It is retryable on cleanup failure.
// Active callers receive connection-loss/cancellation from the real peer;
// their later lease releases are safe. No implicit background mode.
func(m *Manager)Shutdown(ctx context.Context)error{
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closing=true
	if m.closed{return nil}
	if err:=m.stopPeer(ctx);err!=nil{return err}
	if err:=m.lock.Close();err!=nil{return err}
	m.closed=true
	return nil
}
