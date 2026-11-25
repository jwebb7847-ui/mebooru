package acquisitionpolicy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerRefusesUnrecoveredProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir,lockFile), []byte{0,fenceVersion,fenceDirty}, 0o600); err != nil { t.Fatal(err) }
	l, err := LockProfile(dir)
	if err != nil { t.Fatal(err) }
	defer l.Close()
	store := openTestStore(t,dir)
	policy, err := New(store)
	if err != nil { t.Fatal(err) }
	m,err := NewManager(context.Background(),l,policy,func(_, _ context.Context,_ string)(ManagedPeer,error){
		t.Fatal("unexpected launch")
		return nil,errors.New("unexpected launch")
	},LifecycleOptions{})
	if m != nil || !errors.Is(err,ErrWorkerRecoveryRequired) { t.Fatalf("unrecovered profile: %v",err) }
}

func TestManagerFencesBeforeStartupAndClearsAfterConfirmedStop(t *testing.T) {
	ctx := context.Background()
	var log []string
	p := &lifecyclePeer{fakePeer:fakePeer{log:&log}}
	var dir string
	var m *Manager
	starts := 0
	manager,_,profile := newTestManager(t,func(_, _ context.Context,generation string)(ManagedPeer,error){
		starts++
		// Inspect the already-owned descriptor, not a separate Windows handle.
		record,err := m.lock.readFence()
		if err != nil || record.version!=custodyFenceVersion || record.state!=fenceDirty || record.generationID()!=generation { t.Fatalf("not fenced before launch: %v %v",record,err) }
		return p,nil
	})
	m=manager
	dir=profile
	lease,err := m.Acquire(ctx)
	if err != nil { t.Fatal(err) }
	lease.Release()
	p.stopErr=errors.New("unconfirmed containment")
	if err:=m.Shutdown(ctx);!errors.Is(err,p.stopErr){t.Fatalf("stop: %v",err)}
	if m.fence==nil{t.Fatal("failed cleanup discarded fence")}
	p.stopErr=nil
	if err:=m.Shutdown(ctx);err!=nil{t.Fatal(err)}
	if starts!=1{t.Fatal("unexpected restart")}
	body,err:=os.ReadFile(filepath.Join(dir,lockFile))
	if err!=nil||len(body)!=3+generationSize||body[0]!=0||body[1]!=custodyFenceVersion||body[2]!=fenceClean{t.Fatalf("clean record: %v %v",body,err)}
}

func TestManagerClearsNoChildStartupFailure(t *testing.T){
	ctx:=context.Background()
	startErr:=errors.New("verified no child")
	m,now,_:=newTestManager(t,func(_, _ context.Context,_ string)(ManagedPeer,error){return nil,startErr})
	if _,err:=m.Acquire(ctx);!errors.Is(err,startErr){t.Fatal(err)}
	if m.fence!=nil{t.Fatal("no-child failure retained fence")}
	*now=now.Add(2*time.Second)
	if _,err:=m.Acquire(ctx);!errors.Is(err,startErr){t.Fatal(err)}
}
