package acquisitionpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Disposable worker exercises the real protocol and gate without any
// media, HTTP listener, network operation, application profile or child
// descendants. Its drain acknowledgement applies ONLY to this fixture.
func TestLifecycleIPCFixture(t *testing.T){
	if os.Getenv("MEBOORU_LIFECYCLE_FIXTURE")!="1"{return}
	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
	defer cancel()
	gate:=workerproto.NewNetworkGate(ctx)
	defer gate.Close()
	mode:=os.Getenv("MEBOORU_LIFECYCLE_ACK")
	s,err:=workerproto.OpenSession(ctx,os.Stdin,os.Stdout,workerproto.Worker,func(ctx context.Context,op workerproto.Operation,payload json.RawMessage)(json.RawMessage,*workerproto.Fault){
		switch op{
		case workerproto.SetOffline:
			policy,err:=workerproto.DecodeOfflinePolicy(payload)
			if err!=nil{return nil,&workerproto.Fault{Code:"invalid"}}
			if err:=gate.SetOffline(policy.Offline);err!=nil{return nil,&workerproto.Fault{Code:"offline"}}
			if mode=="bad"{return json.RawMessage("{}"),nil}
			if mode=="undrained"&&policy.Offline{return json.RawMessage("{\"offline\":true,\"drained\":false}"),nil}
			ack,_:=json.Marshal(workerproto.OfflineAck{Offline:policy.Offline,Drained:policy.Offline})
			return ack,nil
		case workerproto.Health:
			_,finish,err:=gate.Admit(ctx)
			if err==nil{finish()}
			if err!=nil{return json.RawMessage("{\"online\":false}"),nil}
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
	if err!=nil{os.Exit(2)}
	<-s.Done()
	os.Exit(0)
}

func fixtureStarter(t *testing.T,mode string,started *int)Starter{
	t.Helper()
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	return func(start,lifetime context.Context,_ string)(ManagedPeer,error){
		(*started)++
		// Test-only direct child: there are deliberately no descendants.
		// Production must substitute contained process cleanup.
		p,err:=workerproto.StartOwnedProcess(lifetime,workerproto.ProcessOptions{
			Executable:exe,Args:[]string{"-test.run=^TestLifecycleIPCFixture$"},
			Env:append(os.Environ(),"MEBOORU_LIFECYCLE_FIXTURE=1","MEBOORU_LIFECYCLE_ACK="+mode),
			StartupTimeout:10*time.Second,StopTimeout:4*time.Second,
		},nil)
		if err!=nil{return nil,err}
		peer,err:=NewSessionPeer(p.Session,p.Stop)
		if err!=nil{_ = p.Stop(start);return nil,err}
		return peer,nil
	}
}

func TestLifecycleWithRealPrivateSession(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),25*time.Second)
	defer cancel()
	starts:=0
	m,now,_:=newTestManager(t,fixtureStarter(t,"normal",&starts))
	lease,err:=m.Acquire(ctx)
	if err!=nil{t.Fatal(err)}
	defer lease.Release()
	peer:=lease.Peer.(*SessionPeer)
	check:=func(online bool){
		t.Helper()
		e,err:=peer.session.Call(ctx,workerproto.Health,nil)
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
	*now=now.Add(2*time.Minute)
	if err:=m.Tick(ctx,false);err!=nil{t.Fatal(err)}
	select{case <-peer.Done():default:t.Fatal("idle session still running")}
	if err:=m.Tick(ctx,true);err!=nil||starts!=2{t.Fatalf("scheduled wake: %v",err)}
	if m.EffectiveOnline(){t.Fatal("idle restart lost offline policy")}
	if err:=m.Shutdown(ctx);err!=nil{t.Fatal(err)}
}

func TestLifecycleRejectsInvalidOrUndrainedAcknowledgement(t *testing.T){
	for _,mode:=range []string{"bad","undrained"}{
		t.Run(mode,func(t *testing.T){
			ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
			defer cancel()
			starts:=0
			m,_,_:=newTestManager(t,fixtureStarter(t,mode,&starts))
			if _,err:=m.Acquire(ctx);!errors.Is(err,ErrBadAcknowledgement){t.Fatalf("invalid ack: %v",err)}
			if m.EffectiveOnline(){t.Fatal("invalid ack granted online")}
			if starts!=1{t.Fatalf("unexpected starts %d",starts)}
			if err:=m.Shutdown(ctx);err!=nil{t.Fatal(err)}
		})
	}
}
