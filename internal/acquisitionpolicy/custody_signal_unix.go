//go:build darwin || linux

package acquisitionpolicy

import (
	"os/exec"
	"syscall"
)

func isolateCustodyProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Terminal Ctrl-C belongs to the library. The custodian must stay
	// alive to drain its children and write the terminal receipt.
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
}
