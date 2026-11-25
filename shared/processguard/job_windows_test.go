//go:build windows

package processguard

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestWindowsJobFixture(t *testing.T){
	switch os.Getenv("MEBOORU_GUARD_FIXTURE"){
	case "worker":
		// Do not create descendants until the original process handle has
		// been successfully assigned and the parent explicitly activates.
		var one [1]byte
		if n,err:=os.Stdin.Read(one[:]);n!=1||err!=nil||one[0]!=1{os.Exit(2)}
		child:=fixtureCommand(t,"TestGuardGrandchild","grandchild")
		child.Stdout=os.Stdout
		if err:=child.Start();err!=nil{os.Exit(3)}
		time.Sleep(30*time.Second)
		os.Exit(8)
	case "parent":
		job,err:=NewJob()
		if err!=nil{os.Exit(3)}
		child:=fixtureCommand(t,"TestWindowsJobFixture","worker")
		gate,err:=child.StdinPipe()
		if err!=nil{os.Exit(3)}
		child.Stdout=os.Stdout
		if err:=child.Start();err!=nil{os.Exit(3)}
		if err:=job.Assign(child.Process);err!=nil{
			_ = child.Process.Kill()
			_ = child.Wait()
			os.Exit(3)
		}
		if _,err:=gate.Write([]byte{1});err!=nil{os.Exit(3)}
		_ = gate.Close()
		var one [1]byte
		_,_=os.Stdin.Read(one[:])
		// No Close: prove the OS revokes the job on owner process exit.
		os.Exit(6)
	default:
		return
	}
}

func TestWindowsJobWorkerLossAndContainment(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
	defer cancel()
	job,err:=NewJob()
	if err!=nil{t.Fatal(err)}
	defer job.Close()
	cmd:=fixtureCommand(t,"TestWindowsJobFixture","worker")
	gate,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	defer gate.Close()
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close()
	defer w.Close()
	cmd.Stdout=w
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	_ = w.Close()
	if err:=job.Assign(cmd.Process);err!=nil{t.Fatal(err)}
	if _,err:=gate.Write([]byte{1});err!=nil{t.Fatal(err)}
	_ = gate.Close()
	reader:=witnessReady(t,ctx,r)
	if err:=cmd.Process.Kill();err!=nil{t.Fatal(err)}
	_ = cmd.Wait()
	waited=true
	// Direct worker death is insufficient; explicitly terminate the job.
	if err:=job.TerminateAndWait(ctx);err!=nil{t.Fatal(err)}
	witnessEOF(t,ctx,reader)
}

func TestWindowsJobOwnerLoss(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
	defer cancel()
	cmd:=fixtureCommand(t,"TestWindowsJobFixture","parent")
	gate,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	defer gate.Close()
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close()
	defer w.Close()
	cmd.Stdout=w
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	_ = w.Close()
	reader:=witnessReady(t,ctx,r)
	if err:=cmd.Process.Kill();err!=nil{t.Fatal(err)}
	_ = cmd.Wait()
	waited=true
	witnessEOF(t,ctx,reader)
}
