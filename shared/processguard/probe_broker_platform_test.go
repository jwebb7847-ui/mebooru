//go:build darwin || linux || windows

package processguard

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// This is the full broker channel, actual platform guards/jobs and
// grandchild witnesses. The broker resides in the test custodian, not
// the real app. No executable paths or commands cross the protocol.
func TestProbeBrokerContainedOperations(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	t.Cleanup(func(){
		stop,done:=context.WithTimeout(context.Background(),10*time.Second)
		defer done()
		if err:=set.SealAndStop(stop);err!=nil{t.Errorf("cleanup: %v",err)}
	})
	unrelated:=unrelatedWitness(t,ctx)
	ar,aw,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer ar.Close();defer aw.Close()
	br,bw,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer br.Close();defer bw.Close()
	outputs:=[]*os.File{aw,bw}
	next:=0
	s,c,done:=openProbeFixture(t,ctx,set,func(context.Context)(OperationChild,error){
		if next>=len(outputs){return nil,ErrOperationLimit}
		w:=outputs[next]
		next++
		child,err:=startContainedOperationFixture(t,w)
		_ = w.Close()
		return child,err
	})
	a,err:=c.Start(ctx)
	if err!=nil{t.Fatal(err)}
	if _,err:=c.Start(ctx);err!=nil{t.Fatal(err)}
	ra:=witnessReady(t,ctx,ar)
	rb:=witnessReady(t,ctx,br)
	if err:=c.Stop(ctx,a);err!=nil{t.Fatal(err)}
	witnessEOF(t,ctx,ra)
	// Observe the second operation still alive before closing the
	// control channel. EOF after cleanup is independently checked.
	bEOF:=make(chan error,1)
	go func(){_,err:=rb.ReadByte();bEOF<-err}()
	select{
	case err:=<-bEOF:t.Fatalf("sibling stopped early: %v",err)
	case <-time.After(150*time.Millisecond):
	}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
	select{
	case err:=<-bEOF:if err!=io.EOF{t.Fatalf("witness: %v",err)}
	case <-ctx.Done():t.Fatal(ctx.Err())
	}
	unrelated()
}
