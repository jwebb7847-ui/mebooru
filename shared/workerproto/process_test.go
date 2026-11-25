package workerproto

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestOwnedWorkerFixture(t *testing.T) {
	mode:=os.Getenv("MEBOORU_OWNED_FIXTURE")
	switch mode {
	case "stall":
		// No handshake. The parent must enforce its startup deadline.
		time.Sleep(time.Minute)
		os.Exit(9)
	case "refuse", "normal":
	default:
		return
	}
	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
	defer cancel()
	s,err:=OpenSession(ctx,os.Stdin,os.Stdout,Worker,func(ctx context.Context,op Operation,_ json.RawMessage)(json.RawMessage,*Fault){
		switch op{
		case Health:
			return json.RawMessage("{\"ready\":true}"),nil
		case Shutdown:
			if mode=="refuse"{return nil,&Fault{Code:"busy"}}
			return nil,nil
		default:
			return nil,&Fault{Code:"unsupported"}
		}
	})
	if err!=nil{os.Exit(2)}
	<-s.Done()
	os.Exit(0)
}

func ownedOptions(t *testing.T, mode string) ProcessOptions {
	t.Helper()
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	return ProcessOptions{
		Executable:exe,
		Args:[]string{"-test.run=^TestOwnedWorkerFixture$"},
		Env:append(os.Environ(),"MEBOORU_OWNED_FIXTURE="+mode),
		StartupTimeout:10*time.Second,
		StopTimeout:4*time.Second,
	}
}

func TestOwnedProcessStartCallStop(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
	defer cancel()
	p,err:=StartOwnedProcess(ctx,ownedOptions(t,"normal"),nil)
	if err!=nil{t.Fatal(err)}
	t.Cleanup(func(){_ = p.Stop(context.Background())})
	e,err:=p.Session.Call(ctx,Health,nil)
	if err!=nil||e.Error!=nil||string(e.Payload)!="{\"ready\":true}"{t.Fatalf("health: %+v %v",e,err)}
	if err:=p.Stop(ctx);err!=nil{t.Fatal(err)}
	if err:=p.Stop(ctx);err!=nil{t.Fatalf("repeat stop: %v",err)}
	if err:=p.Wait(ctx);err!=nil{t.Fatal(err)}
}

func TestOwnedProcessHandshakeDeadline(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	opts:=ownedOptions(t,"stall")
	opts.StartupTimeout=500*time.Millisecond
	p,err:=StartOwnedProcess(ctx,opts,nil)
	if p!=nil || !errors.Is(err,ErrStartupTimeout){t.Fatalf("expected startup timeout, got %v %v",p,err)}
}

func TestOwnedProcessRefusalAndParentCancellation(t *testing.T){
	for _,mode:=range []string{"refuse","normal"}{
		t.Run(mode,func(t *testing.T){
			ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
			defer cancel()
			p,err:=StartOwnedProcess(ctx,ownedOptions(t,mode),nil)
			if err!=nil{t.Fatal(err)}
			t.Cleanup(func(){_ = p.Stop(context.Background())})
			if mode=="refuse"{
				if err:=p.Stop(ctx);!errors.Is(err,ErrForcedStop)||!errors.Is(err,ErrShutdownRefused){t.Fatalf("refusal: %v",err)}
			}else{
				cancel()
			}
			select{
			case <-p.Done():
			case <-time.After(10*time.Second):t.Fatal("owned child not reaped")
			}
		})
	}
}
