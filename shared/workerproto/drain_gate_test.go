package workerproto

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDrainGateCancellationIsNotCompletion(t *testing.T){
	ctx:=context.Background()
	g:=NewDrainGate(ctx)
	if _,_,err:=g.Admit(ctx);!errors.Is(err,ErrOffline){t.Fatal(err)}
	if err:=g.Open();err!=nil{t.Fatal(err)}
	caller,cancel:=context.WithCancel(ctx)
	a,finishA,err:=g.Admit(caller)
	if err!=nil{t.Fatal(err)}
	b,finishB,err:=g.Admit(ctx)
	if err!=nil{t.Fatal(err)}
	cancel()
	select{case <-a.Done():case <-time.After(time.Second):t.Fatal("caller cancel not propagated")}
	if b.Err()!=nil{t.Fatal("one operation canceled unrelated work")}
	short,stop:=context.WithTimeout(ctx,20*time.Millisecond)
	defer stop()
	if err:=g.Drain(short);!errors.Is(err,context.DeadlineExceeded){t.Fatalf("cancellation treated as proof: %v",err)}
	if b.Err()==nil{t.Fatal("offline did not revoke other operation")}
	if err:=g.Open();!errors.Is(err,ErrDrainPending){t.Fatalf("reopened undrained: %v",err)}
	finishA(nil)
	finishA(nil)
	if err:=g.Open();!errors.Is(err,ErrDrainPending){t.Fatalf("duplicate completion cleared other lease: %v",err)}
	finishB(nil)
	if err:=g.Drain(ctx);err!=nil{t.Fatal(err)}
	if err:=g.Open();err!=nil{t.Fatal(err)}
	next,done,err:=g.Admit(ctx)
	if err!=nil{t.Fatal(err)}
	if next.Err()!=nil||a.Err()==nil||b.Err()==nil{t.Fatal("old grant revived")}
	done(nil)
	if err:=g.Seal(ctx);err!=nil{t.Fatal(err)}
	if err:=g.Open();!errors.Is(err,ErrOffline){t.Fatalf("sealed gate reopened: %v",err)}
}

func TestDrainGateCleanupFailurePoisonsSession(t *testing.T){
	ctx:=context.Background()
	g:=NewDrainGate(ctx)
	if err:=g.Open();err!=nil{t.Fatal(err)}
	_,fail,err:=g.Admit(ctx)
	if err!=nil{t.Fatal(err)}
	other,finish,err:=g.Admit(ctx)
	if err!=nil{t.Fatal(err)}
	fail(errors.New("descendants remain"))
	if other.Err()==nil{t.Fatal("cleanup failure did not revoke other work")}
	finish(nil)
	if err:=g.Drain(ctx);!errors.Is(err,ErrDrainUnconfirmed){t.Fatalf("uncertain cleanup acknowledged: %v",err)}
	if err:=g.Open();err==nil{t.Fatal("poisoned session reopened")}
	if _,_,err:=g.Admit(ctx);!errors.Is(err,ErrOffline){t.Fatal(err)}
}

func TestDrainGateWaitsForExplicitCompletion(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second)
	defer cancel()
	g:=NewDrainGate(ctx)
	if err:=g.Open();err!=nil{t.Fatal(err)}
	op,finish,err:=g.Admit(ctx)
	if err!=nil{t.Fatal(err)}
	drained:=make(chan error,1)
	go func(){drained<-g.Drain(ctx)}()
	select{case <-op.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
	select{case err:=<-drained:t.Fatalf("early drain: %v",err);default:}
	if err:=g.Open();!errors.Is(err,ErrDrainPending){t.Fatal(err)}
	finish(nil)
	select{case err:=<-drained:if err!=nil{t.Fatal(err)};case <-ctx.Done():t.Fatal(ctx.Err())}
	if err:=g.Open();err!=nil{t.Fatal(err)}
	cancel()
	if _,_,err:=g.Admit(context.Background());!errors.Is(err,ErrOffline){t.Fatal(err)}
}
