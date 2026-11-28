//go:build !darwin && !linux && !windows

package acquisitionpolicy

import "os/exec"

func isolateCustodyProcess(*exec.Cmd) {}
