//go:build darwin || linux

package processguard

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestUnixGuardFixture(t *testing.T){
	mode:=os.Getenv("MEBOORU_GUARD_FIXTURE")
	switch mode{
	case "guard","guard-crash":
		childMode:="worker"
		if mode=="guard-crash"{childMode="worker-crash"}
		child:=fixtureCommand(t,"TestUnixGuardFixture",childMode)
		child.Stdout=os.Stdout
		if err:=RunGuard(os.Stdin,child);err!=nil{
			_,_=os.Stderr.WriteString(err.Error())
		}
		os.Exit(2)
	case "worker","worker-crash":
		life:=os.NewFile(3,"guard-liveness")
		done,err:=WatchGuard(life)
		if err!=nil{os.Exit(3)}
		child:=fixtureCommand(t,"TestGuardGrandchild","grandchild")
		child.Stdout=os.Stdout
		readyR,readyW,err:=os.Pipe()
		if err!=nil{os.Exit(4)}
		child.ExtraFiles=[]*os.File{readyW}
		child.Env=append(child.Env,"MEBOORU_GRANDCHILD_READY_FD=1")
		if err:=child.Start();err!=nil{os.Exit(4)}
		_ = readyW.Close()
		var ready [1]byte
		if n,err:=readyR.Read(ready[:]);n!=1||err!=nil||ready[0]!=1{os.Exit(4)}
		_ = readyR.Close()
		if mode=="worker-crash"{os.Exit(7)}
		select{
		case <-done:os.Exit(5)
		case <-time.After(30*time.Second):os.Exit(8)
		}
	case "parent":
		// This process is a disposable stand-in for the library. Exiting
		// without cleanup must close the guard's liveness pipe.
		lifeR,lifeW,err:=os.Pipe()
		if err!=nil{os.Exit(2)}
		child:=fixtureCommand(t,"TestUnixGuardFixture","guard")
		child.Stdin=lifeR
		child.Stdout=os.Stdout
		if err:=PrepareGuard(child);err!=nil{os.Exit(2)}
		if err:=child.Start();err!=nil{os.Exit(2)}
		_ = lifeR.Close()
		var one [1]byte
		_,_=os.Stdin.Read(one[:])
		// Keep lifeW live until this explicit unclean exit.
		_ = lifeW.Fd()
		os.Exit(6)
	default:
		return
	}
}

func TestUnixGuardParentAndWorkerLoss(t *testing.T){
	for _,mode:=range []string{"parent-loss","worker-loss","guard-loss"}{
		t.Run(mode,func(t *testing.T){
			ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
			defer cancel()
			lifeR,lifeW,err:=os.Pipe()
			if err!=nil{t.Fatal(err)}
			defer lifeR.Close()
			defer lifeW.Close()
			witnessR,witnessW,err:=os.Pipe()
			if err!=nil{t.Fatal(err)}
			defer witnessR.Close()
			defer witnessW.Close()
			fixtureMode:="guard"
			if mode=="worker-loss"{fixtureMode="guard-crash"}
			cmd:=fixtureCommand(t,"TestUnixGuardFixture",fixtureMode)
			cmd.Stdin=lifeR
			cmd.Stdout=witnessW
			if err:=PrepareGuard(cmd);err!=nil{t.Fatal(err)}
			if err:=cmd.Start();err!=nil{t.Fatal(err)}
			waited:=false
			defer func(){if !waited{_ = lifeW.Close();_ = cmd.Process.Kill();_ = cmd.Wait()}}()
			_ = lifeR.Close()
			_ = witnessW.Close()
			reader:=witnessReady(t,ctx,witnessR)
			switch mode{
			case "parent-loss":
				_ = lifeW.Close()
			case "guard-loss":
				if err:=cmd.Process.Kill();err!=nil{t.Fatal(err)}
			}
			witnessEOF(t,ctx,reader)
			err=cmd.Wait()
			waited=true
			if err==nil{t.Fatal("guard did not terminate its group")}
		})
	}
}

func TestUnixGuardAbruptParentExit(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
	defer cancel()
	unrelated:=unrelatedWitness(t,ctx)
	cmd:=fixtureCommand(t,"TestUnixGuardFixture","parent")
	control,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	r,w,err:=os.Pipe()
	if err!=nil{_ = control.Close();t.Fatal(err)}
	defer r.Close()
	defer w.Close()
	cmd.Stdout=w
	if err:=cmd.Start();err!=nil{_ = control.Close();t.Fatal(err)}
	waited:=false
	defer func(){_ = control.Close();if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	_ = w.Close()
	reader:=witnessReady(t,ctx,r)
	_ = control.Close()
	witnessEOF(t,ctx,reader)
	err=cmd.Wait()
	waited=true
	if exit,ok:=err.(*exec.ExitError);!ok||exit.ExitCode()!=6{t.Fatalf("unexpected parent exit: %v",err)}
	unrelated()
}
