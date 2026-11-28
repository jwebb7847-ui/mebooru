//go:build !linux && !darwin && !windows

package main

import (
	"os"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)
func managedProbeChannels([]string)(*os.File,*os.File,*os.File,*os.File,error){return nil,nil,nil,nil,processguard.ErrInheritedPipes}
