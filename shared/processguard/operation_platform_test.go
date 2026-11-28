//go:build darwin || linux || windows

package processguard

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestIndependentContainedOperations(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),25*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	defer func(){
		cleanup,stop:=context.WithTimeout(context.Background(),10*time.Second)
		defer stop()
		if err:=set.SealAndStop(cleanup);err!=nil{t.Errorf("final cleanup: %v",err)}
	}()
	unrelated:=unrelatedWitness(t,ctx)
	start:=func()(*OperationLease,*os.File){
		t.Helper()
		r,w,err:=os.Pipe()
		if err!=nil{t.Fatal(err)}
		t.Cleanup(func(){_ = r.Close();_ = w.Close()})
		l,err:=set.Start(ctx,func(context.Context)(OperationChild,error){return startContainedOperationFixture(t,w)})
		_ = w.Close()
		if err!=nil{t.Fatal(err)}
		return l,r
	}
	a,ar:=start()
	_,br:=start()
	ra:=witnessReady(t,ctx,ar)
	rb:=witnessReady(t,ctx,br)
	if err:=a.Stop(ctx);err!=nil{t.Fatal(err)}
	witnessEOF(t,ctx,ra)
	// The other operation's pipe must remain open until its own stop.
	// A pending read provides a witness without changing the child.
	bEOF:=make(chan error,1)
	go func(){_,err:=rb.ReadByte();bEOF<-err}()
	select{
	case err:=<-bEOF:t.Fatalf("unrelated operation terminated: %v",err)
	case <-time.After(150*time.Millisecond):
	}
	if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
	select{
	case err:=<-bEOF:if !errors.Is(err,io.EOF){t.Fatalf("expected EOF, got %v",err)}
	case <-ctx.Done():t.Fatal(ctx.Err())
	}
	unrelated()
}
