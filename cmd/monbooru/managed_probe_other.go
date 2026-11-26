//go:build !darwin && !linux && !windows

package main

import (
	"os"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)
func startManagedProbeWorker(*os.File,*os.File)(acquisitionpolicy.CustodyChild,error){return nil,processguard.ErrProbeExecutable}
func runManagedGuardProbe()error{return processguard.ErrProbeExecutable}
