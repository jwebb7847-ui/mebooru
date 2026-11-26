package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

const crashPending="crash-cleanup-pending"
const crashRelease="crash-allow-cleanup"

func waitCrashMarker(ctx context.Context,dir,name string)error{
	tick:=time.NewTicker(20*time.Millisecond)
	defer tick.Stop()
	for{
		info,err:=os.Lstat(filepath.Join(dir,name))
		if err==nil{
			if !info.Mode().IsRegular(){return processguard.ErrProbeExecutable}
			return nil
		}
		if !errors.Is(err,os.ErrNotExist){return err}
		select{case <-ctx.Done():return ctx.Err();case <-tick.C:}
	}
}

// Test-only pause BEFORE stopping the remaining independent operation.
// Never bypass the underlying containment proof, including on timeout.
// OperationSet serializes Stop calls for this exact lease.
type crashHeldOperation struct{
	processguard.OperationChild
	dir string
}
func(p *crashHeldOperation)Stop(ctx context.Context)error{
	err:=os.WriteFile(filepath.Join(p.dir,crashPending),nil,0600)
	if err==nil{
		barrier,done:=context.WithTimeout(ctx,6*time.Second)
		err=waitCrashMarker(barrier,p.dir,crashRelease)
		done()
	}
	if err!=nil{
		cleanup,done:=context.WithTimeout(context.Background(),5*time.Second)
		defer done()
		return errors.Join(err,p.OperationChild.Stop(cleanup))
	}
	return p.OperationChild.Stop(ctx)
}
func crashProbeStarter(dir string)processguard.ProbeStarter{
	count:=0
	return func(ctx context.Context)(processguard.OperationChild,error){
		count++
		child,err:=startOperationProbe(ctx)
		if child!=nil&&count==2{return &crashHeldOperation{OperationChild:child,dir:dir},err}
		return child,err
	}
}

// Open a fresh manager against a known disposable profile. The generation
// pointer is populated only by the manager's trusted starter.
func crashProbeManager(ctx context.Context,dir,mode string)(*acquisitionpolicy.Manager,*acquisitionpolicy.Store,*string,error){
	lock,err:=acquisitionpolicy.LockProfile(dir)
	if err!=nil{return nil,nil,nil,err}
	store,err:=acquisitionpolicy.Open(dir)
	if err!=nil{_ = lock.Close();return nil,nil,nil,err}
	policy,err:=acquisitionpolicy.New(store)
	if err!=nil{_ = store.Close();_ = lock.Close();return nil,nil,nil,err}
	self,err:=os.Executable()
	if err==nil{self,err=filepath.EvalSymlinks(self)}
	if err!=nil{_ = store.Close();_ = lock.Close();return nil,nil,nil,err}
	generation:=new(string)
	m,err:=acquisitionpolicy.NewManager(ctx,lock,policy,func(start,lifetime context.Context,f *acquisitionpolicy.WorkerFence)(acquisitionpolicy.ManagedPeer,error){
		*generation=f.Generation()
		cmd:=exec.Command(self,mode,dir,*generation)
		cmd.Stderr=os.Stderr
		p,err:=acquisitionpolicy.StartCustodySession(start,lifetime,cmd,f,nil)
		if p==nil{return nil,err}
		return p,err
	},acquisitionpolicy.LifecycleOptions{})
	if err!=nil{_ = store.Close();_ = lock.Close();return nil,nil,nil,err}
	return m,store,generation,nil
}

func checkCrashHealth(ctx context.Context,m *acquisitionpolicy.Manager,l *acquisitionpolicy.Lease,online bool)error{
	p,ok:=l.Peer.(*acquisitionpolicy.CustodySessionPeer)
	if !ok{return processguard.ErrProbeExecutable}
	e,err:=p.Session().Call(ctx,workerproto.Health,nil)
	if err!=nil{return err}
	var h struct{Probe bool;Offline bool;Operations int}
	want:=0
	if online{want=1}
	if e.Error!=nil||json.Unmarshal(e.Payload,&h)!=nil||!h.Probe||h.Offline==online||h.Operations!=want||m.EffectiveOnline()!=online{return errors.New("crash probe health mismatch")}
	return nil
}

func runCrashProbeOwner(dir string)(result error){
	if err:=validateManagedProbeDir(dir);err!=nil{return err}
	ctx,cancel:=context.WithTimeout(context.Background(),35*time.Second)
	defer cancel()
	m,store,generation,err:=crashProbeManager(ctx,dir,"internal-managed-crash-custody-probe-v1")
	if err!=nil{return err}
	defer store.Close()
	defer func(){
		stop,done:=context.WithTimeout(context.Background(),20*time.Second)
		defer done()
		result=errors.Join(result,m.Shutdown(stop))
	}()
	lease,err:=m.Acquire(ctx)
	if err!=nil{return err}
	defer lease.Release()
	if err:=m.SetOffline(ctx,false);err!=nil{return err}
	if err:=checkCrashHealth(ctx,m,lease,true);err!=nil{return err}
	// The online ack means two fixed leaves started and one was
	// stopped with proof. The other has signaled actual readiness.
	if _,err:=fmt.Fprintln(os.Stdout,*generation);err!=nil{return err}
	done:=make(chan struct{})
	go func(){var b [1]byte;_,_=os.Stdin.Read(b[:]);close(done)}()
	select{case <-ctx.Done():return ctx.Err();case <-done:return nil}
}

func waitCrashReceipt(ctx context.Context,dir,generation string)error{
	tick:=time.NewTicker(20*time.Millisecond)
	defer tick.Stop()
	want:="mebooru-acquisition-cleanup-v1\n"+generation+"\n"
	for{
		body,err:=os.ReadFile(filepath.Join(dir,"acquisition-cleanup.receipt"))
		if err==nil{
			if string(body)!=want{return errors.New("unexpected crash recovery receipt")}
			return nil
		}
		if !errors.Is(err,os.ErrNotExist){return err}
		select{case <-ctx.Done():return ctx.Err();case <-tick.C:}
	}
}

func rejectCrashReplacement(dir string)error{
	lock,err:=acquisitionpolicy.LockProfile(dir)
	if err!=nil{return err}
	f,err:=lock.PrepareCustody()
	if f!=nil||!errors.Is(err,acquisitionpolicy.ErrWorkerRecoveryRequired){
		// Never clear a surprising fence merely to make a test pass.
		return errors.Join(errors.New("replacement not refused before cleanup"),err,lock.Close())
	}
	return lock.Close()
}

func recoverCrashProbe(ctx context.Context,dir,oldGeneration string)(result error){
	m,store,generation,err:=crashProbeManager(ctx,dir,"internal-managed-custody-probe-v1")
	if err!=nil{return err}
	defer store.Close()
	defer func(){
		stop,done:=context.WithTimeout(context.Background(),20*time.Second)
		defer done()
		result=errors.Join(result,m.Shutdown(stop))
	}()
	offline,err:=store.Load()
	if err!=nil||offline{return errors.Join(errors.New("crash probe persisted consent lost"),err)}
	// Explicit test policy change before any replacement worker starts.
	if err:=m.SetOffline(ctx,true);err!=nil{return err}
	lease,err:=m.Acquire(ctx)
	if err!=nil{return err}
	defer lease.Release()
	if *generation==""||*generation==oldGeneration{return errors.New("replacement reused generation")}
	if err:=checkCrashHealth(ctx,m,lease,false);err!=nil{return err}
	if err:=m.SetOffline(ctx,false);err!=nil{return err}
	if err:=checkCrashHealth(ctx,m,lease,true);err!=nil{return err}
	if err:=m.SetOffline(ctx,true);err!=nil{return err}
	if err:=checkCrashHealth(ctx,m,lease,false);err!=nil{return err}
	if err:=m.Shutdown(ctx);err!=nil{return err}
	return waitCrashReceipt(ctx,dir,*generation)
}

// Controller kills only its own direct library fixture, never a
// looked-up PID. The custodian and operation guards remain independent.
func runExecutableCrashProbe()(result error){
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	temp,err:=filepath.EvalSymlinks(os.TempDir())
	if err!=nil{return err}
	dir,err:=os.MkdirTemp(temp,managedProbePrefix)
	if err!=nil{return err}
	safe:=false
	defer func(){
		if safe{result=errors.Join(result,os.RemoveAll(dir))}else{
			_ = os.WriteFile(filepath.Join(dir,crashRelease),nil,0600)
			fmt.Fprintln(os.Stderr,"retained disposable crash profile:",dir)
		}
	}()
	ctx,cancel:=context.WithTimeout(context.Background(),65*time.Second)
	defer cancel()
	cmd,err:=operationProbeCommand("internal-managed-crash-owner-probe-v1")
	if err!=nil{return err}
	cmd.Args=append(cmd.Args,dir)
	cmd.Stderr=os.Stderr
	in,err:=cmd.StdinPipe()
	if err!=nil{return err}
	defer in.Close()
	r,w,err:=os.Pipe()
	if err!=nil{return err}
	defer r.Close();defer w.Close()
	cmd.Stdout=w
	if err:=cmd.Start();err!=nil{return err}
	_ = w.Close()
	exited:=make(chan error,1)
	go func(){exited<-cmd.Wait()}()
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();<-exited}}()
	ready:=make(chan error,1)
	var generation [65]byte
	go func(){_,err:=io.ReadFull(r,generation[:]);ready<-err}()
	start,done:=context.WithTimeout(ctx,20*time.Second)
	defer done()
	select{
	case err:=<-ready:if err!=nil{return err}
	case <-start.Done():return start.Err()
	}
	old:=string(generation[:64])
	if generation[64]!='\n'{return processguard.ErrProbeExecutable}
	if _,err:=hex.DecodeString(old);err!=nil{return err}
	if err:=cmd.Process.Kill();err!=nil{return err}
	select{
	case err:=<-exited:waited=true;if err==nil{return errors.New("crash owner exited normally")}
	case <-ctx.Done():return ctx.Err()
	}
	if err:=waitCrashMarker(ctx,dir,crashPending);err!=nil{return err}
	if err:=rejectCrashReplacement(dir);err!=nil{return err}
	if _,err:=os.Lstat(filepath.Join(dir,"acquisition-cleanup.receipt"));!errors.Is(err,os.ErrNotExist){return errors.New("receipt published before operation cleanup")}
	if err:=os.WriteFile(filepath.Join(dir,crashRelease),nil,0600);err!=nil{return err}
	if err:=waitCrashReceipt(ctx,dir,old);err!=nil{return err}
	if err:=recoverCrashProbe(ctx,dir,old);err!=nil{return err}
	safe=true
	fmt.Println("PASS: managed executable crash recovery (active operation)")
	return nil
}
