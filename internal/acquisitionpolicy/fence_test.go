package acquisitionpolicy

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWorkerFenceCleanReopenAndStaleCapability(t *testing.T) {
	dir := t.TempDir()
	l, err := LockProfile(dir)
	if err != nil { t.Fatal(err) }
	defer l.Close()
	first, err := l.BeginWorker()
	if err != nil { t.Fatal(err) }
	if f, err := l.BeginWorker(); f != nil || !errors.Is(err, ErrWorkerRecoveryRequired) {
		t.Fatalf("second launch: %v", err)
	}
	if err := l.Close(); !errors.Is(err, ErrWorkerRecoveryRequired) { t.Fatalf("live close: %v", err) }
	if err := first.ConfirmStopped(); err != nil { t.Fatal(err) }
	second, err := l.BeginWorker()
	if err != nil { t.Fatal(err) }
	if err := first.ConfirmStopped(); !errors.Is(err, ErrStaleWorkerFence) { t.Fatalf("stale: %v", err) }
	if err := second.ConfirmStopped(); err != nil { t.Fatal(err) }
	if err := l.Close(); err != nil { t.Fatal(err) }
	reopened, err := LockProfile(dir)
	if err != nil { t.Fatal(err) }
	defer reopened.Close()
	f, err := reopened.BeginWorker()
	if err != nil { t.Fatal(err) }
	if err := f.ConfirmStopped(); err != nil { t.Fatal(err) }
}

func TestWorkerFenceMalformedFailsClosed(t *testing.T) {
	for _, record := range [][]byte{{0}, {0, fenceVersion}, {0, 2, fenceClean}, {0, fenceVersion, 'X'}, {0, fenceVersion, fenceClean, 0}, {0, fenceVersion, fenceDirty}} {
		dir := t.TempDir()
		path := filepath.Join(dir, lockFile)
		if err := os.WriteFile(path, record, 0o600); err != nil { t.Fatal(err) }
		l, err := LockProfile(dir)
		if err != nil { t.Fatal(err) }
		f, err := l.BeginWorker()
		if f != nil || !errors.Is(err, ErrWorkerRecoveryRequired) { t.Fatalf("record %v: %v", record, err) }
		if err := l.Close(); err != nil { t.Fatal(err) }
		body, err := os.ReadFile(path)
		if err != nil || string(body) != string(record) { t.Fatalf("record changed: %v", err) }
	}
}

func TestWorkerFenceOwnerFixture(t *testing.T) {
	dir := os.Getenv("MEBOORU_TEST_FENCE_PROFILE")
	if dir == "" { return }
	l, err := LockProfile(dir)
	if err != nil { os.Exit(2) }
	f, err := l.BeginWorker()
	if err != nil { os.Exit(3) }
	_, _ = os.Stdout.WriteString("ready\n")
	var one [1]byte
	_, _ = os.Stdin.Read(one[:])
	runtime.KeepAlive(f)
	runtime.KeepAlive(l)
	os.Exit(7)
}

func TestWorkerFenceSurvivesAbruptOwnerLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil { t.Fatal(err) }
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestWorkerFenceOwnerFixture$")
	cmd.Env = append(os.Environ(), "MEBOORU_TEST_FENCE_PROFILE="+dir)
	in, err := cmd.StdinPipe()
	if err != nil { t.Fatal(err) }
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil { t.Fatal(err) }
	if err := cmd.Start(); err != nil { t.Fatal(err) }
	waited := false
	defer func(){ if !waited { _ = cmd.Process.Kill(); _ = cmd.Wait() } }()
	ready := make(chan string, 1)
	go func(){ line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" { t.Fatalf("fixture: %q", line) }
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := cmd.Process.Kill(); err != nil { t.Fatal(err) }
	_ = cmd.Wait()
	waited = true
	// The advisory lock is available again, but the durable fence is not.
	l, err := LockProfile(dir)
	if err != nil { t.Fatal(err) }
	defer l.Close()
	if f, err := l.BeginWorker(); f != nil || !errors.Is(err, ErrWorkerRecoveryRequired) {
		t.Fatalf("replacement launch after abrupt loss: %v", err)
	}
}
