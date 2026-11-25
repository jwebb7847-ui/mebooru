//go:build windows

package processguard

import (
	"bufio"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestWindowsDormantJobFixture(t *testing.T){
	if os.Getenv("MEBOORU_GUARD_FIXTURE")!="dormant-job-worker"{return}
	if err:=AwaitJobActivation(os.Stdin);err!=nil{os.Exit(2)}
	child:=fixtureCommand(t,"TestGuardGrandchild","grandchild")
	child.Stdout=os.Stdout
	if err:=child.Start();err!=nil{os.Exit(3)}
	time.Sleep(30*time.Second)
	os.Exit(8)
}

func TestWindowsDormantJobSupervisor(t *testing.T){
	for _,arm:=range []bool{false,true}{
		name:="never-armed"
		if arm{name="armed"}
		t.Run(name,func(t *testing.T){
			ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
			defer cancel()
			unrelated:=unrelatedWitness(t,ctx)
			cmd:=fixtureCommand(t,"TestWindowsDormantJobFixture","dormant-job-worker")
			r,w,err:=os.Pipe()
			if err!=nil{t.Fatal(err)}
			defer r.Close()
			defer w.Close()
			cmd.Stdout=w
			job,err:=StartDormantJob(cmd)
			if job!=nil{defer job.Stop(ctx)}
			if err!=nil{t.Fatal(err)}
			_ = w.Close()
			if _,err:=job.ProtocolInput();!errors.Is(err,ErrJobActivation){t.Fatalf("unarmed input: %v",err)}
			reader:=bufio.NewReader(r)
			if arm{
				if err:=job.Activate();err!=nil{t.Fatal(err)}
				if err:=job.Activate();!errors.Is(err,ErrJobActivation){t.Fatalf("second activation: %v",err)}
				if _,err:=job.ProtocolInput();err!=nil{t.Fatal(err)}
				reader=witnessReady(t,ctx,r)
			}
			if err:=job.Stop(ctx);err!=nil{t.Fatal(err)}
			witnessEOF(t,ctx,reader)
			if err:=job.Activate();!errors.Is(err,ErrJobActivation){t.Fatalf("activation after stop: %v",err)}
			if err:=job.Stop(ctx);err!=nil{t.Fatal(err)}
			unrelated()
		})
	}
}

// Simulate custodian loss in the pre-assignment window. Without the
// activation byte even a non-job-contained trusted worker exits on EOF
// before creating any descendants.
func TestWindowsDormantWorkerPreAssignmentEOF(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	cmd:=fixtureCommand(t,"TestWindowsDormantJobFixture","dormant-job-worker")
	in,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	defer in.Close()
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close()
	defer w.Close()
	cmd.Stdout=w
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	_ = w.Close()
	_ = in.Close()
	witnessEOF(t,ctx,bufio.NewReader(r))
	err=cmd.Wait()
	waited=true
	if err==nil{t.Fatal("unexpected successful unarmed worker")}
}
