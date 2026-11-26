//go:build windows

package main

import (
	"os"
	"os/exec"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

func startManagedProbeWorker(r,w,br,bw *os.File)(acquisitionpolicy.CustodyChild,error){
	path,err:=processguard.ProbeSibling("monbooru","monloader")
	if err!=nil{return nil,err}
	cmd:=exec.Command(path,"internal-managed-worker-probe-v1")
	cmd.Stderr=os.Stderr
	job,err:=processguard.StartDormantJobWithBroker(cmd,r,w,br,bw)
	if job==nil{return nil,err}
	if err!=nil{return job,err}
	return job,job.Activate()
}
func runManagedGuardProbe()error{return processguard.ErrProbeExecutable}

func startOperationProbePlatform(w *os.File)(processguard.OperationChild,error){
	cmd,err:=operationProbeCommand("internal-managed-operation-leaf-probe-v1")
	if err!=nil{return nil,err}
	cmd.Stdout,cmd.Stderr=w,os.Stderr
	job,err:=processguard.StartDormantJob(cmd)
	if job==nil{return nil,err}
	return job,err
}
func runOperationProbeGuard()error{return processguard.ErrProbeExecutable}
func runOperationProbeLeaf()error{
	if err:=processguard.AwaitJobActivation(os.Stdin);err!=nil{return err}
	return operationProbeLeaf()
}
