//go:build darwin || linux

package main

import (
	"context"
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
func startManagedProbeWorker(r,w *os.File)(acquisitionpolicy.CustodyChild,error){
	self,err:=os.Executable()
	if err!=nil{return nil,err}
	self,err=filepath.EvalSymlinks(self)
	if err!=nil{return nil,err}
	cmd:=exec.Command(self,"internal-managed-guard-probe-v1")
	cmd.Stdin,cmd.Stdout,cmd.Stderr=r,w,os.Stderr
	g,err:=processguard.StartDormantGuard(cmd)
	if g==nil{return nil,err}
	p:=&probeGuard{g:g}
	if err!=nil{return p,err}
	return p,g.Activate()
}
func runManagedGuardProbe()error{
	path,err:=processguard.ProbeSibling("monbooru","monloader")
	if err!=nil{return err}
	cmd:=exec.Command(path,"internal-managed-worker-probe-v1")
	cmd.Stdin,cmd.Stdout,cmd.Stderr=os.Stdin,os.Stdout,os.Stderr
	return processguard.RunDormantGuard(os.NewFile(3,"custody-liveness"),cmd)
}
