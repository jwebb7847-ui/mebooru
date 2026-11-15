package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/monbooru/monbooru/internal/shared/desktop"
	"github.com/monbooru/monbooru/internal/shared/logx"
)

func launchMeloader(binary, configPath string) (func(), error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, fmt.Errorf("start Meloader separately on %s", runtime.GOOS)
	}
	if binary == "" || configPath == "" {
		return nil, fmt.Errorf("Meloader launch requires both an executable and a config path")
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Meloader config must be a regular file")
	}
	cmd := exec.Command(binary, "-desktop=false", "-config", configPath)
	desktop.HideConsole(cmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := cmd.Wait(); err != nil {
			logx.Warnf("Meloader exited: %v", err)
		}
	}()
	return sync.OnceFunc(func() {
		select {
		case <-done:
			return
		default:
		}
		// Keep the library serving while Meloader drains any active ingest.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-done
		}
	}), nil
}
