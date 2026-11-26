//go:build !darwin && !linux && !windows

package main

import (
	"os"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)
func startManagedProbeWorker(*os.File,*os.File,*os.File,*os.File,bool)(acquisitionpolicy.CustodyChild,error){return nil,processguard.ErrProbeExecutable}
func runManagedGuardProbe(bool)error{return processguard.ErrProbeExecutable}

func startOperationProbePlatform(*os.File)(processguard.OperationChild,error){return nil,processguard.ErrProbeExecutable}
func runOperationProbeGuard()error{return processguard.ErrProbeExecutable}
func runOperationProbeLeaf()error{return processguard.ErrProbeExecutable}

func startRuntimeVersionPlatform(*os.File,string)(processguard.OperationChild,error){return nil,errRuntimeProbe}
func runRuntimeVersionGuard(string)error{return errRuntimeProbe}
func runRuntimeVersionPlatformLeaf(string)error{return errRuntimeProbe}
