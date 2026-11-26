//go:build darwin || linux || windows

package processguard

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestBrokerRequesterProcessFixture(t *testing.T){
	if os.Getenv("MEBOORU_GUARD_FIXTURE")!="broker-requester"{return}
	r,w,err:=OpenInheritedPipes(os.Getenv("MEBOORU_TEST_BROKER_PIPES"))
	if err!=nil{os.Exit(2)}
	ctx,cancel:=context.WithTimeout(context.Background(),35*time.Second)
	defer cancel()
	s,err:=workerproto.OpenSession(ctx,r,w,workerproto.OperationRequester,nil)
	if err!=nil{os.Exit(3)}
	defer s.Close()
	client,err:=workerproto.NewProbeClient(s)
	if err!=nil{os.Exit(4)}
	if _,err:=client.Start(ctx);err!=nil{os.Exit(5)}
	if _,err:=client.Start(ctx);err!=nil{os.Exit(6)}
	_,_ = os.Stdout.WriteString("ready\n")
	select{
	case <-ctx.Done():os.Exit(7)
	case <-s.Done():os.Exit(8)
	}
}

// Kill the actual requester process, rather than simulating peer loss
// with Close in the same process. The test custodian owns the groups;
// the dead requester never held any operation liveness writer.
func TestProbeBrokerAbruptRequesterLoss(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),40*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	defer func(){
		stop,done:=context.WithTimeout(context.Background(),10*time.Second)
		defer done()
		if err:=set.SealAndStop(stop);err!=nil{t.Errorf("cleanup: %v",err)}
	}()
	unrelated:=unrelatedWitness(t,ctx)
	pipe:=func()(*os.File,*os.File){
		t.Helper()
		r,w,err:=os.Pipe()
		if err!=nil{t.Fatal(err)}
		t.Cleanup(func(){_ = r.Close();_ = w.Close()})
		return r,w
	}
	requestRead,serverWrite:=pipe()
	serverRead,requestWrite:=pipe()
	ar,aw:=pipe()
	br,bw:=pipe()
	readyRead,readyWrite:=pipe()
	cmd:=fixtureCommand(t,"TestBrokerRequesterProcessFixture","broker-requester")
	token,release,err:=AttachInheritedPipes(cmd,requestRead,requestWrite)
	if err!=nil{t.Fatal(err)}
	defer release()
	cmd.Env=append(cmd.Env,"MEBOORU_TEST_BROKER_PIPES="+token)
	cmd.Stdout=readyWrite
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	release()
	_ = requestRead.Close()
	_ = requestWrite.Close()
	_ = readyWrite.Close()

	outputs:=[]*os.File{aw,bw}
	next:=0
	done:=make(chan error,1)
	go func(){
		done<-ServeProbeBroker(ctx,serverRead,serverWrite,set,func(context.Context)(OperationChild,error){
			if next>=len(outputs){return nil,ErrOperationLimit}
			out:=outputs[next]
			next++
			child,err:=startContainedOperationFixture(t,out)
			_ = out.Close()
			return child,err
		})
	}()
	witnessReady(t,ctx,readyRead)
	a:=witnessReady(t,ctx,ar)
	b:=witnessReady(t,ctx,br)
	if err:=cmd.Process.Kill();err!=nil{t.Fatal(err)}
	_ = cmd.Wait()
	waited=true
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
	witnessEOF(t,ctx,a)
	witnessEOF(t,ctx,b)
	unrelated()
}
