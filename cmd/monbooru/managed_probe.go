package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

const managedProbePrefix="mebooru-managed-probe-"

func dispatchManagedProbe(args []string)bool{
	if len(args)==0{return false}
	var err error
	switch args[0]{
	case "internal-managed-probe-v1":
		if len(args)!=1{err=processguard.ErrProbeExecutable}else{err=runManagedProbe()}
	case "internal-managed-crash-probe-v1":
		if len(args)!=1{err=processguard.ErrProbeExecutable}else{err=runExecutableCrashProbe()}
	case "internal-managed-crash-owner-probe-v1":
		if len(args)!=2{err=processguard.ErrProbeExecutable}else{err=runCrashProbeOwner(args[1])}
	case "internal-managed-custody-probe-v1", "internal-managed-crash-custody-probe-v1":
		if len(args)!=4{err=processguard.ErrProbeExecutable}else{err=runManagedCustodyProbe(args[1],args[2],args[3],args[0]=="internal-managed-crash-custody-probe-v1")}
	case "internal-managed-operation-guard-probe-v1":
		if len(args)!=1{err=processguard.ErrProbeExecutable}else{err=runOperationProbeGuard()}
	case "internal-managed-operation-leaf-probe-v1":
		if len(args)!=1{err=processguard.ErrProbeExecutable}else{err=runOperationProbeLeaf()}
	case "internal-managed-guard-probe-v1":
		if len(args)!=1{err=processguard.ErrProbeExecutable}else{err=runManagedGuardProbe()}
	default:return false
	}
	if err!=nil{fmt.Fprintln(os.Stderr,"managed executable probe:",err);os.Exit(2)}
	return true
}

// Probe accepts no external profile or executable path. All policy and
// ownership state is disposable; no normal app startup is entered.
func runManagedProbe()(result error){
	if runtime.GOOS!="linux"&&runtime.GOOS!="darwin"&&runtime.GOOS!="windows"{return processguard.ErrProbeExecutable}
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	self,err:=os.Executable()
	if err!=nil{return err}
	self,err=filepath.EvalSymlinks(self)
	if err!=nil{return err}
	temp,err:=filepath.EvalSymlinks(os.TempDir())
	if err!=nil{return err}
	dir,err:=os.MkdirTemp(temp,managedProbePrefix)
	if err!=nil{return err}
	// Preserve evidence on cleanup failure rather than deleting a fence
	// while process ownership is uncertain.
	safe:=true
	defer func(){
		if safe{result=errors.Join(result,os.RemoveAll(dir))}else{fmt.Fprintln(os.Stderr,"retained disposable recovery profile:",dir)}
	}()
	lock,err:=acquisitionpolicy.LockProfile(dir)
	if err!=nil{return err}
	defer lock.Close()
	store,err:=acquisitionpolicy.Open(dir)
	if err!=nil{return err}
	defer store.Close()
	policy,err:=acquisitionpolicy.New(store)
	if err!=nil{return err}
	ctx,cancel:=context.WithTimeout(context.Background(),45*time.Second)
	defer cancel()
	starts:=0
	m,err:=acquisitionpolicy.NewManager(ctx,lock,policy,func(start,lifetime context.Context,f *acquisitionpolicy.WorkerFence)(acquisitionpolicy.ManagedPeer,error){
		starts++
		cmd:=exec.Command(self,"internal-managed-custody-probe-v1",dir,f.Generation())
		cmd.Stderr=os.Stderr
		p,err:=acquisitionpolicy.StartCustodySession(start,lifetime,cmd,f,nil)
		if p==nil{return nil,err}
		return p,err
	},acquisitionpolicy.LifecycleOptions{IdleAfter:time.Millisecond,RestartDelay:time.Millisecond})
	if err!=nil{return err}
	defer func(){
		stop,done:=context.WithTimeout(context.Background(),20*time.Second)
		defer done()
		if err:=m.Shutdown(stop);err!=nil{safe=false;result=errors.Join(result,err)}
	}()
	lease,err:=m.Acquire(ctx)
	if err!=nil{return err}
	defer lease.Release()
	p:=lease.Peer.(*acquisitionpolicy.CustodySessionPeer)
	check:=func(offline bool)error{
		e,err:=p.Session().Call(ctx,workerproto.Health,nil)
		if err!=nil{return err}
		var h struct{Probe bool; Offline bool; Operations int}
		if e.Error!=nil||json.Unmarshal(e.Payload,&h)!=nil||!h.Probe||h.Offline!=offline||m.EffectiveOnline()==offline||(offline&&h.Operations!=0)||(!offline&&h.Operations!=1){return errors.New("probe health mismatch")}
		return nil
	}
	if err:=check(true);err!=nil{return err}
	if err:=m.SetOffline(ctx,false);err!=nil{return err}
	if err:=check(false);err!=nil{return err}
	// Explicitly refuse actual acquisition even with simulated consent.
	e,err:=p.Session().Call(ctx,workerproto.QueueEnqueue,json.RawMessage("{}"))
	if err!=nil||e.Error==nil||e.Error.Code!="unsupported"{return errors.New("probe accepted acquisition")}
	if err:=m.SetOffline(ctx,true);err!=nil{return err}
	if err:=check(true);err!=nil{return err}
	lease.Release()
	time.Sleep(10*time.Millisecond)
	if err:=m.Tick(ctx,false);err!=nil{return err}
	if err:=m.Tick(ctx,true);err!=nil{return err}
	if starts!=2{return errors.New("probe did not restart on demand")}
	if err:=m.SetOffline(ctx,false);err!=nil{return err}
	if err:=m.Shutdown(ctx);err!=nil{return err}
	fmt.Println("PASS: managed executable probe (no acquisition)")
	return nil
}

func validateManagedProbeDir(dir string)error{
	temp,err:=filepath.EvalSymlinks(os.TempDir())
	if err!=nil{return err}
	// Restrict this diagnostic to an immediate, nonsymlink temporary
	// probe directory. It is not an authorization boundary against
	// arbitrary same-user programs and must not be used for real profiles.
	if !filepath.IsAbs(dir)||filepath.Dir(dir)!=temp||!strings.HasPrefix(filepath.Base(dir),managedProbePrefix){return processguard.ErrProbeExecutable}
	info,err:=os.Lstat(dir)
	if err!=nil||!info.IsDir()||info.Mode()&os.ModeSymlink!=0{return processguard.ErrProbeExecutable}
	return nil
}

func runManagedCustodyProbe(dir,generation,token string,holdCleanup bool)error{
	if err:=validateManagedProbeDir(dir);err!=nil{return err}
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	r,w,err:=processguard.OpenInheritedPipes(token)
	if err!=nil{return err}
	defer r.Close()
	defer w.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),45*time.Second)
	defer cancel()
	return acquisitionpolicy.RunCustodianReady(ctx,dir,generation,os.Stdin,os.Stdout,func(start context.Context)(acquisitionpolicy.CustodyChild,error){
		if err:=start.Err();err!=nil{return nil,err}
		set:=processguard.NewOperationSet(2)
		br,bw,err:=os.Pipe()
		if err!=nil{return nil,err}
		rr,rw,err:=os.Pipe()
		if err!=nil{_ = br.Close();_ = bw.Close();return nil,err}
		primary,err:=startManagedProbeWorker(r,w,br,rw)
		_ = r.Close();_ = w.Close();_ = br.Close();_ = rw.Close()
		if primary==nil{_ = rr.Close();_ = bw.Close();return nil,err}
		brokerCtx,stopBroker:=context.WithCancel(ctx)
		done:=make(chan struct{})
		owned:=&probeCustody{primary:primary,set:set,stopBroker:stopBroker,brokerDone:done,done:make(chan struct{})}
		startOperation:=processguard.ProbeStarter(startOperationProbe)
		if holdCleanup{
			// Only the second fixed leaf (the one still active after the
			// online transition) participates in the crash-test barrier.
			startOperation=crashProbeStarter(dir)
		}
		go func(){owned.brokerErr=processguard.ServeProbeBroker(brokerCtx,rr,bw,set,startOperation);close(done)}()
		go func(){select{case <-primary.Done():case <-done:};close(owned.done)}()
		return owned,err
	})
}
