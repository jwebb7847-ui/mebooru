//go:build darwin || linux

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

type probeGuard struct{g *processguard.DormantGuard}
func(p *probeGuard)Done()<-chan struct{}{return p.g.Done()}
func(p *probeGuard)Stop(ctx context.Context)error{
	// Wait briefly for protocol EOF and cooperative direct-worker reap.
	// The group-absence check remains the actual cleanup proof.
	t:=time.NewTimer(3*time.Second)
	defer t.Stop()
	select{case <-p.g.Done():case <-t.C:case <-ctx.Done():}
	return p.g.Stop(ctx)
}
func startManagedProbeWorker(r,w,br,bw *os.File,abortWorker bool)(acquisitionpolicy.CustodyChild,error){
	self,err:=os.Executable()
	if err!=nil{return nil,err}
	self,err=filepath.EvalSymlinks(self)
	if err!=nil{return nil,err}
	mode:="internal-managed-guard-probe-v1"
	if abortWorker{mode="internal-managed-worker-loss-guard-probe-v1"}
	cmd:=exec.Command(self,mode)
	cmd.Stdin,cmd.Stdout,cmd.Stderr=r,w,os.Stderr
	g,err:=processguard.StartDormantGuardWithBroker(cmd,br,bw)
	if g==nil{return nil,err}
	p:=&probeGuard{g:g}
	if err!=nil{return p,err}
	return p,g.Activate()
}
func runManagedGuardProbe(abortWorker bool)error{
	path,err:=processguard.ProbeSibling("monbooru","monloader")
	if err!=nil{return err}
	mode:="internal-managed-worker-probe-v1"
	if abortWorker{mode="internal-managed-worker-loss-probe-v1"}
	cmd:=exec.Command(path,mode)
	cmd.Stdin,cmd.Stdout,cmd.Stderr=os.Stdin,os.Stdout,os.Stderr
	br,bw,err:=processguard.AdoptGuardBrokerPipes()
	if err!=nil{return err}
	defer br.Close();defer bw.Close()
	return processguard.RunDormantGuardWithBroker(os.NewFile(3,"custody-liveness"),cmd,br,bw)
}

type probeOperationGuard struct{*processguard.DormantGuard;input io.WriteCloser}
func(p *probeOperationGuard)Stop(ctx context.Context)error{
	_ = p.input.Close()
	t:=time.NewTimer(3*time.Second)
	defer t.Stop()
	select{case <-p.Done():case <-t.C:case <-ctx.Done():}
	return p.DormantGuard.Stop(ctx)
}
func startOperationProbePlatform(w *os.File)(processguard.OperationChild,error){
	cmd,err:=operationProbeCommand("internal-managed-operation-guard-probe-v1")
	if err!=nil{return nil,err}
	in,err:=cmd.StdinPipe()
	if err!=nil{return nil,err}
	cmd.Stdout,cmd.Stderr=w,os.Stderr
	g,err:=processguard.StartDormantGuard(cmd)
	if g==nil{_ = in.Close();return nil,err}
	return &probeOperationGuard{DormantGuard:g,input:in},err
}
func runOperationProbeGuard()error{
	cmd,err:=operationProbeCommand("internal-managed-operation-leaf-probe-v1")
	if err!=nil{return err}
	cmd.Stdin,cmd.Stdout,cmd.Stderr=os.Stdin,os.Stdout,os.Stderr
	return processguard.RunDormantGuard(os.NewFile(3,"custody-liveness"),cmd)
}
func runOperationProbeLeaf()error{
	done,err:=processguard.WatchGuard(os.NewFile(3,"guard-liveness"))
	if err!=nil{return err}
	go func(){<-done;os.Exit(3)}()
	return operationProbeLeaf()
}

func startRuntimeVersionPlatform(w *os.File,digest string)(processguard.OperationChild,error){
	cmd,err:=operationProbeCommand("internal-managed-runtime-version-guard-probe-v1")
	if err!=nil{return nil,err}
	cmd.Args=append(cmd.Args,digest)
	cmd.Stdout,cmd.Stderr=w,os.Stderr
	g,err:=processguard.StartDormantGuard(cmd)
	if g==nil{return nil,err}
	return g,err
}
func runRuntimeVersionGuard(digest string)error{
	cmd,err:=operationProbeCommand("internal-managed-runtime-version-leaf-probe-v1")
	if err!=nil{return err}
	cmd.Args=append(cmd.Args,digest)
	cmd.Stdout,cmd.Stderr=os.Stdout,os.Stderr
	return processguard.RunDormantGuard(os.NewFile(3,"custody-liveness"),cmd)
}
func runRuntimeVersionPlatformLeaf(digest string)error{
	done,err:=processguard.WatchGuard(os.NewFile(3,"guard-liveness"))
	if err!=nil{return err}
	go func(){<-done;os.Exit(3)}()
	return runRuntimeVersionLeaf(digest)
}

func startRuntimeStreamPlatform(w,status *os.File,digest string)(processguard.OperationChild,error){
	cmd,err:=operationProbeCommand("internal-managed-runtime-stream-guard-probe-v1")
	if err!=nil{return nil,err}
	cmd.Args=append(cmd.Args,digest)
	cmd.Stdout,cmd.Stderr=w,status
	g,err:=processguard.StartDormantGuard(cmd)
	if g==nil{return nil,err}
	return g,err
}
func runRuntimeStreamGuard(digest string)error{
	cmd,err:=operationProbeCommand("internal-managed-runtime-stream-leaf-probe-v1")
	if err!=nil{return err}
	cmd.Args=append(cmd.Args,digest)
	cmd.Stdout,cmd.Stderr=os.Stdout,os.Stderr
	return processguard.RunDormantGuard(os.NewFile(3,"custody-liveness"),cmd)
}
func runRuntimeStreamPlatformLeaf(digest string)error{
	done,err:=processguard.WatchGuard(os.NewFile(3,"guard-liveness"))
	if err!=nil{return err}
	go func(){<-done;os.Exit(3)}()
	return runRuntimeStreamLeaf(digest)
}
