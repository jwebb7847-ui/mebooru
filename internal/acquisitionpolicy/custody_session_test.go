package acquisitionpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func sessionFixtureCommand(mode,dir,generation string)*exec.Cmd{
	exe,err:=os.Executable()
	if err!=nil{panic(err)}
	cmd:=exec.Command(exe,"-test.run=^TestCustodySessionFixture$","--")
	cmd.Env=append(os.Environ(),"MEBOORU_TEST_SESSION_MODE="+mode,"MEBOORU_TEST_CUSTODY_DIR="+dir,"MEBOORU_TEST_CUSTODY_GENERATION="+generation)
	cmd.Stderr=os.Stderr
	return cmd
}

func TestCustodySessionFixture(t *testing.T){
	mode:=os.Getenv("MEBOORU_TEST_SESSION_MODE")
	dir:=os.Getenv("MEBOORU_TEST_CUSTODY_DIR")
	gen:=os.Getenv("MEBOORU_TEST_CUSTODY_GENERATION")
	switch mode{
	case "custodian":
		r,w,err:=processguard.OpenInheritedPipes(os.Args[len(os.Args)-1])
		if err!=nil{os.Exit(2)}
		ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
		err=RunCustodianReady(ctx,dir,gen,os.Stdin,os.Stdout,func(context.Context)(CustodyChild,error){
			child,err:=startSessionFixtureWorker(dir,r,w)
			_ = r.Close()
			_ = w.Close()
			return child,err
		})
		cancel()
		_ = r.Close()
		_ = w.Close()
		if err!=nil{os.Exit(3)}
		os.Exit(0)
	case "worker","guard":
		runSessionFixtureWorker(mode,dir)
	}
}

func serveSessionFixture(r,w *os.File){
	ctx,cancel:=context.WithTimeout(context.Background(),25*time.Second)
	defer cancel()
	gate:=workerproto.NewNetworkGate(ctx)
	defer gate.Close()
	session,err:=workerproto.OpenSession(ctx,r,w,workerproto.Worker,func(ctx context.Context,op workerproto.Operation,payload json.RawMessage)(json.RawMessage,*workerproto.Fault){
		switch op{
		case workerproto.SetOffline:
			p,err:=workerproto.DecodeOfflinePolicy(payload)
			if err!=nil||gate.SetOffline(p.Offline)!=nil{return nil,&workerproto.Fault{Code:"invalid"}}
			ack,_:=json.Marshal(workerproto.OfflineAck{Offline:p.Offline,Drained:p.Offline})
			return ack,nil
		case workerproto.Health:
			_,finish,err:=gate.Admit(ctx)
			if err!=nil{return json.RawMessage("{\"online\":false}"),nil}
			finish()
			return json.RawMessage("{\"online\":true}"),nil
		case workerproto.ReserveIdle:
			_ = gate.SetOffline(true)
			return json.RawMessage("{\"reserved\":true}"),nil
		case workerproto.Shutdown:
			gate.Close()
			return nil,nil
		default:return nil,&workerproto.Fault{Code:"unsupported"}
		}
	})
	if err!=nil{os.Exit(4)}
	<-session.Done()
	os.Exit(0)
}

func TestCustodySessionManagerLifecycle(t *testing.T){
	if runtime.GOOS!="darwin"&&runtime.GOOS!="linux"&&runtime.GOOS!="windows"{t.Skip("unsupported platform")}
	ctx,cancel:=context.WithTimeout(context.Background(),45*time.Second)
	defer cancel()
	dir:=t.TempDir()
	lock,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer lock.Close()
	store:=openTestStore(t,dir)
	policy,err:=New(store)
	if err!=nil{t.Fatal(err)}
	starts:=0
	m,err:=NewManager(ctx,lock,policy,func(start,lifetime context.Context,f *WorkerFence)(ManagedPeer,error){
		starts++
		p,err:=StartCustodySession(start,lifetime,sessionFixtureCommand("custodian",dir,f.Generation()),f,nil)
		if p==nil{return nil,err}
		return p,err
	},LifecycleOptions{IdleAfter:time.Second,RestartDelay:time.Second})
	if err!=nil{t.Fatal(err)}
	defer func(){cleanup,stop:=context.WithTimeout(context.Background(),20*time.Second);defer stop();if err:=m.Shutdown(cleanup);err!=nil{t.Errorf("cleanup: %v",err)}}()
	now:=time.Now()
	m.now=func()time.Time{return now}
	lease,err:=m.Acquire(ctx)
	if err!=nil{t.Fatal(err)}
	defer lease.Release()
	p:=lease.Peer.(*CustodySessionPeer)
	check:=func(online bool){
		t.Helper()
		e,err:=p.Session().Call(ctx,workerproto.Health,nil)
		want:="{\"online\":false}"
		if online{want="{\"online\":true}"}
		if err!=nil||e.Error!=nil||string(e.Payload)!=want||m.EffectiveOnline()!=online{t.Fatalf("health: %+v %v",e,err)}
	}
	check(false)
	if err:=m.SetOffline(ctx,false);err!=nil{t.Fatal(err)}
	check(true)
	if err:=m.SetOffline(ctx,true);err!=nil{t.Fatal(err)}
	check(false)
	lease.Release()
	now=now.Add(2*time.Second)
	if err:=m.Tick(ctx,false);err!=nil{t.Fatal(err)}
	if m.fence!=nil{t.Fatal("idle cleanup did not clear fence")}
	if err:=m.Tick(ctx,true);err!=nil{t.Fatal(err)}
	if starts!=2{t.Fatal("fresh demand did not restart")}
	if err:=m.Shutdown(ctx);err!=nil{t.Fatal(err)}
	if m.EffectiveOnline(){t.Fatal("quit retained online grant")}
	if _,err:=m.Acquire(ctx);!errors.Is(err,ErrLifecycleClosed){t.Fatalf("quit: %v",err)}
}
