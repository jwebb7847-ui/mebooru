//go:build darwin || linux

package processguard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
)

// DormantGuard owns a direct guard process and exclusive liveness writer.
// A caller must retain this handle on errors and obtain Stop proof before
// releasing its worker fence. It is not yet a production IPC launcher.
type DormantGuard struct {
	mu sync.Mutex
	group int
	verified bool
	life *os.File
	armed bool
	done chan struct{}
	waitErr error
}

// StartDormantGuard starts only the trusted guard entry, with liveness
// fd 3 and the caller's existing stdin/stdout reserved for future IPC.
// No worker may start until Activate. If this returns a nonnil guard and
// an error, the caller still owns cleanup. The command must not use a
// shell or be controlled by protocol, browser, or other untrusted input.
func StartDormantGuard(cmd *exec.Cmd) (*DormantGuard, error) {
	if cmd == nil || !filepath.IsAbs(cmd.Path) || len(cmd.ExtraFiles) != 0 {
		return nil, ErrGroupIdentity
	}
	if err := PrepareGuard(cmd); err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	cmd.ExtraFiles = []*os.File{r}
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		return nil, err
	}
	_ = r.Close()
	g := &DormantGuard{group: cmd.Process.Pid, life: w, done: make(chan struct{})}
	// The explicit Setpgid at exec is primary; check the live group too.
	group, err := syscall.Getpgid(cmd.Process.Pid)
	g.verified = err == nil && group == g.group && group > 1
	go func() {
		g.waitErr = cmd.Wait()
		close(g.done)
	}()
	if !g.verified {
		// No activation was attempted. Retain the handle until the
		// direct dormant guard has been reaped.
		g.closeLife()
		return g, errors.Join(ErrGroupIdentity, err)
	}
	return g, nil
}

func (g *DormantGuard) Done() <-chan struct{} { return g.done }

// GroupID is an identity from this launch, not a reusable termination
// target or durable recovery credential. Persist validated local recovery
// identity before activating the guard.
func (g *DormantGuard) GroupID() (int, error) {
	if !g.verified {
		return 0, ErrGroupIdentity
	}
	return g.group, nil
}

// Activate must be called only after the library records ownership and
// verifies containment prerequisites. A failed write is uncertain: even
// if it delivered the byte, Stop still requires group disappearance.
func (g *DormantGuard) Activate() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.verified || g.armed || g.life == nil {
		return ErrActivationRefused
	}
	select {
	case <-g.done:
		return ErrActivationRefused
	default:
	}
	g.armed = true
	n, err := g.life.Write([]byte{ActivationByte})
	if n != 1 || err != nil {
		g.closeLife()
		return errors.Join(ErrActivationRefused, err)
	}
	return nil
}

// closeLife is called with mu held (or before publication).
func (g *DormantGuard) closeLife() {
	if g.life != nil {
		_ = g.life.Close()
		g.life = nil
	}
}

// Stop revokes parent liveness, reaps the direct guard, then checks that
// its established process group is gone if activation was ever attempted.
// A nonzero guard exit is expected on group termination and is separate
// from cleanup proof. No signal is sent to a numeric group ID. Retry
// after timeout; never discard the handle or report success on uncertainty.
func (g *DormantGuard) Stop(ctx context.Context) error {
	g.mu.Lock()
	g.closeLife()
	armed := g.armed
	g.mu.Unlock()
	select {
	case <-g.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if armed {
		return WaitGroupGone(ctx, g.group)
	}
	return nil
}

// ExitError reports the direct guard exit after reaping, independently
// of descendant cleanup. In particular, nil here is NOT Stop proof.
func (g *DormantGuard) ExitError(ctx context.Context) error {
	select {
	case <-g.done:
		return g.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
