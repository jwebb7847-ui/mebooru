package acquisitionpolicy

import (
	"context"
	"errors"
	"testing"
	"time"
)

type lifecyclePeer struct{
	fakePeer
	idle bool
	idleErr error
	reservations int
}

func(p *lifecyclePeer)ReserveIdle(context.Context)(bool,error){
	p.reservations++
	return p.idle,p.idleErr
}
func(p *lifecyclePeer)Stop(ctx context.Context)error{
	err:=p.fakePeer.Stop(ctx)
	if err==nil{
		select{case <-p.Done():default:close(p.done)}
	}
	return err
}

func newTestManager(t *testing.T,start Starter)(*Manager,*time.Time,string){
	t.Helper()
	dir:=t.TempDir()
	lock,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	store:=openTestStore(t,dir)
	policy,err:=New(store)
	if err!=nil{t.Fatal(err)}
	m,err:=NewManager(context.Background(),lock,policy,start,LifecycleOptions{IdleAfter:time.Minute,RestartDelay:time.Second})
	if err!=nil{_ = lock.Close();t.Fatal(err)}
	now:=time.Now()
	m.now=func()time.Time{return now}
	t.Cleanup(func(){if err:=m.Shutdown(context.Background());err!=nil{t.Errorf("shutdown: %v",err)}})
	return m,&now,dir
}

func TestLifecycleDemandIdleAndFreshSession(t *testing.T){
	ctx:=context.Background()
	var log []string
	var peers []*lifecyclePeer
	m,now,_:=newTestManager(t,func(_, _ context.Context)(ManagedPeer,error){
		p:=&lifecyclePeer{fakePeer:fakePeer{log:&log}}
		peers=append(peers,p)
		return p,nil
	})
	if err:=m.Tick(ctx,false);err!=nil||len(peers)!=0{t.Fatalf("idle startup: %v",err)}
	lease,err:=m.Acquire(ctx)
	if err!=nil{t.Fatal(err)}
	other,err:=m.Acquire(ctx)
	if err!=nil||lease.Peer!=other.Peer||len(peers)!=1{t.Fatalf("duplicate launch: %v",err)}
	*now=now.Add(2*time.Minute)
	if err:=m.Tick(ctx,false);err!=nil||peers[0].reservations!=0{t.Fatalf("active lease idled out: %v",err)}
	lease.Release()
	lease.Release()
	other.Release()
	*now=now.Add(2*time.Minute)
	if err:=m.Tick(ctx,false);err!=nil||peers[0].reservations!=1{t.Fatalf("busy reserve: %v",err)}
	select{case <-peers[0].Done():t.Fatal("busy worker stopped");default:}
	peers[0].idle=true
	*now=now.Add(2*time.Minute)
	if err:=m.Tick(ctx,false);err!=nil{t.Fatal(err)}
	select{case <-peers[0].Done():default:t.Fatal("idle worker remained")}
	if err:=m.Tick(ctx,true);err!=nil||len(peers)!=2{t.Fatalf("scheduled wake: %v",err)}
	if peers[0]==peers[1]{t.Fatal("stale session reused")}
	if err:=m.Shutdown(ctx);err!=nil{t.Fatal(err)}
	if _,err:=m.Acquire(ctx);!errors.Is(err,ErrLifecycleClosed){t.Fatalf("restart after quit: %v",err)}
}

func TestLifecycleCrashBackoffAndFailedCleanup(t *testing.T){
	ctx:=context.Background()
	var log []string
	var peers []*lifecyclePeer
	m,now,dir:=newTestManager(t,func(_, _ context.Context)(ManagedPeer,error){
		p:=&lifecyclePeer{fakePeer:fakePeer{log:&log}}
		peers=append(peers,p)
		return p,nil
	})
	if err:=m.SetOffline(ctx,false);err!=nil{t.Fatal(err)}
	lease,err:=m.Acquire(ctx)
	if err!=nil{t.Fatal(err)}
	if !m.EffectiveOnline(){t.Fatal("explicit consent not applied")}
	close(peers[0].done)
	if m.EffectiveOnline(){t.Fatal("crash retained permission")}
	if _,err:=m.Acquire(ctx);!errors.Is(err,ErrActiveLeases){t.Fatalf("replaced active lost session: %v",err)}
	lease.Release()
	peers[0].stopErr=errors.New("containment unconfirmed")
	if _,err:=m.Acquire(ctx);!errors.Is(err,peers[0].stopErr){t.Fatalf("cleanup: %v",err)}
	if len(peers)!=1{t.Fatal("started replacement before confirmed cleanup")}
	if err:=m.Shutdown(ctx);!errors.Is(err,peers[0].stopErr){t.Fatalf("shutdown: %v",err)}
	if lock,err:=LockProfile(dir);lock!=nil||!errors.Is(err,ErrProfileOwned){if lock!=nil{_ = lock.Close()};t.Fatalf("released profile prematurely: %v",err)}
	peers[0].stopErr=nil
	if err:=m.Shutdown(ctx);err!=nil{t.Fatal(err)}
	lock,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	_ = lock.Close()
	_ = now
}

func TestLifecycleCrashRestartsOnlyOnDemandAfterCooldown(t *testing.T){
	ctx:=context.Background()
	var log []string
	var peers []*lifecyclePeer
	m,now,_:=newTestManager(t,func(_, _ context.Context)(ManagedPeer,error){
		p:=&lifecyclePeer{fakePeer:fakePeer{log:&log}}
		peers=append(peers,p)
		return p,nil
	})
	if err:=m.Tick(ctx,true);err!=nil{t.Fatal(err)}
	close(peers[0].done)
	if err:=m.Tick(ctx,false);err!=nil{t.Fatal(err)}
	if len(peers)!=1{t.Fatal("restarted without demand")}
	if err:=m.Tick(ctx,true);!errors.Is(err,ErrRestartBackoff){t.Fatalf("cooldown: %v",err)}
	*now=now.Add(2*time.Second)
	if err:=m.Tick(ctx,false);err!=nil||len(peers)!=1{t.Fatalf("idle restarted: %v",err)}
	if err:=m.Tick(ctx,true);err!=nil||len(peers)!=2{t.Fatalf("fresh demand: %v",err)}
}

func TestLifecycleStartupFailureRetainsUnconfirmedPeer(t *testing.T){
	ctx:=context.Background()
	var log []string
	startErr:=errors.New("handshake failed")
	stopErr:=errors.New("cleanup failed")
	p:=&lifecyclePeer{fakePeer:fakePeer{log:&log,stopErr:stopErr}}
	starts:=0
	m,_,_:=newTestManager(t,func(_, _ context.Context)(ManagedPeer,error){
		starts++
		return p,startErr
	})
	if _,err:=m.Acquire(ctx);!errors.Is(err,startErr)||!errors.Is(err,stopErr){t.Fatalf("startup: %v",err)}
	if _,err:=m.Acquire(ctx);!errors.Is(err,stopErr){t.Fatalf("replacement: %v",err)}
	if starts!=1{t.Fatal("unconfirmed startup spawned second peer")}
	if err:=m.SetOffline(ctx,false);!errors.Is(err,ErrPeerUnusable){t.Fatalf("unconfirmed startup reauthorized: %v",err)}
	p.stopErr=nil
}
