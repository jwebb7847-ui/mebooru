//go:build windows

package main

import (
	"os"
	"os/exec"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

func startManagedProbeWorker(r,w *os.File)(acquisitionpolicy.CustodyChild,error){
	path,err:=processguard.ProbeSibling("monbooru","monloader")
	if err!=nil{return nil,err}
	cmd:=exec.Command(path,"internal-managed-worker-probe-v1")
	cmd.Stderr=os.Stderr
	job,err:=processguard.StartDormantJobWithPipes(cmd,r,w)
	if job==nil{return nil,err}
	if err!=nil{return job,err}
	return job,job.Activate()
}
func runManagedGuardProbe()error{return processguard.ErrProbeExecutable}
