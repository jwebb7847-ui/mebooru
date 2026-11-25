//go:build darwin || linux

package processguard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestGroupProbeChild(t *testing.T) {
	if os.Getenv("MEBOORU_TEST_GROUP_PROBE") != "1" {
		return
	}
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
	os.Exit(0)
}

func TestWaitGroupGoneRejectsLiveFollowerAfterLeaderExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Both fixture processes are direct children of this test, which
	// guarantees we can reap them even on hosts with unusual PID 1s.
	start := func(group int) (*exec.Cmd, func()) {
		t.Helper()
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestGroupProbeChild$")
		cmd.Env = append(os.Environ(), "MEBOORU_TEST_GROUP_PROBE=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group}
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			_ = in.Close()
			t.Fatal(err)
		}
		waited := false
		t.Cleanup(func() {
			if !waited {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			_ = in.Close()
		})
		return cmd, func() {
			t.Helper()
			_ = in.Close()
			err := cmd.Wait()
			waited = true
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	leader, exitLeader := start(0)
	group := leader.Process.Pid
	follower, exitFollower := start(group)
	unrelated, exitUnrelated := start(0)
	exitLeader()

	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err = WaitGroupGone(short, group)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live group accepted: %v", err)
	}
	if err := syscall.Kill(follower.Process.Pid, 0); err != nil {
		t.Fatalf("probe terminated follower: %v", err)
	}
	exitFollower()
	if err := WaitGroupGone(ctx, group); err != nil {
		t.Fatalf("empty group not confirmed: %v", err)
	}
	if err := syscall.Kill(unrelated.Process.Pid, 0); err != nil {
		t.Fatalf("unrelated process affected: %v", err)
	}
	exitUnrelated()
}

func TestWaitGroupGoneRejectsInvalidIdentity(t *testing.T) {
	for _, group := range []int{-1, 0, 1} {
		if err := WaitGroupGone(context.Background(), group); !errors.Is(err, ErrGroupIdentity) {
			t.Fatalf("invalid group %d: %v", group, err)
		}
	}
}
