package acquisitionpolicy

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func isolateCustodyProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}
