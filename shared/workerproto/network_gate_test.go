package workerproto

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNetworkGateDefaultDenyAndRevocation(t *testing.T){
	parent,cancel:=context.WithCancel(context.Background())
	defer cancel()
	g:=NewNetworkGate(parent)
	defer g.Close()
	if _,_,err:=g.Admit(parent);!errors.Is(err,ErrOffline){t.Fatalf("startup: %v",err)}
	if err:=g.SetOffline(false);err!=nil{t.Fatal(err)}
	first,done,err:=g.Admit(parent)
	if err!=nil{t.Fatal(err)}
	defer done()
	if err:=g.SetOffline(false);err!=nil{t.Fatal(err)}
	if first.Err()!=nil{t.Fatal("idempotent policy update revoked online generation")}
	if err:=g.SetOffline(true);err!=nil{t.Fatal(err)}
	if first.Err()==nil{t.Fatal("old generation not revoked")}
	if _,_,err:=g.Admit(parent);!errors.Is(err,ErrOffline){t.Fatalf("offline: %v",err)}
	if err:=g.SetOffline(false);err!=nil{t.Fatal(err)}
	next,finish,err:=g.Admit(parent)
	if err!=nil{t.Fatal(err)}
	defer finish()
	if next.Err()!=nil||first.Err()==nil{t.Fatal("reopening restored a stale grant")}
	g.Close()
	if next.Err()==nil{t.Fatal("session close did not revoke grant")}
	if err:=g.SetOffline(false);!errors.Is(err,ErrOffline){t.Fatalf("reopen closed gate: %v",err)}
}

func TestNetworkGateCallerAndParentCancellation(t *testing.T){
	parent,cancel:=context.WithCancel(context.Background())
	defer cancel()
	g:=NewNetworkGate(parent)
	defer g.Close()
	if err:=g.SetOffline(false);err!=nil{t.Fatal(err)}
	caller,stop:=context.WithCancel(context.Background())
	defer stop()
	op,finish,err:=g.Admit(caller)
	if err!=nil{t.Fatal(err)}
	defer finish()
	stop()
	select{case <-op.Done():case <-time.After(5*time.Second):t.Fatal("caller cancellation not propagated")}
	next,done,err:=g.Admit(context.Background())
	if err!=nil{t.Fatal(err)}
	defer done()
	cancel()
	if next.Err()==nil{t.Fatal("parent cancellation not propagated")}
	if _,_,err:=g.Admit(context.Background());!errors.Is(err,ErrOffline){t.Fatalf("stale parent: %v",err)}
}
