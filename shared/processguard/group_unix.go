//go:build darwin || linux

package processguard

import (
	"context"
	"errors"
	"math"
	"syscall"
	"time"
)

// WaitGroupGone waits for an already-established owned Unix process group
// to disappear. Call only after revoking launch permission and stopping
// the guard; no trusted managed child may have detached from the group.
// A reaped direct child is not sufficient evidence of descendant exit.
//
// Signal zero is an existence/permission probe, NOT a termination signal.
// Never send a real signal to a stored or potentially reused PGID from
// the supervisor. ESRCH proves absence in this process namespace at that
// instant; reuse can delay success, not authorize killing an unrelated
// group. Other errors (including permission errors) are not proof.
// This is not safe cross-host/cross-namespace crash recovery by itself.
func WaitGroupGone(ctx context.Context, group int) error {
	if group <= 1 || group > math.MaxInt32 {
		return ErrGroupIdentity
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Kill(-group, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
