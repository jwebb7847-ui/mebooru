//go:build darwin || linux

package processguard

import (
	"context"
	"errors"
	"math"
	"runtime"
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
// group. Darwin can transiently report EPERM during descendant reaping.
// Retry it for a bounded interval, but never accept it as proof.
// This is not safe cross-host/cross-namespace crash recovery by itself.
func WaitGroupGone(ctx context.Context, group int) error {
	return waitGroupGone(ctx,group,func(group int)error{return syscall.Kill(-group,0)},runtime.GOOS=="darwin",3*time.Second)
}

// Keep the permission retry budget independent of caller deadlines and
// never renew it. Only ESRCH is success, on either platform.
func waitGroupGone(ctx context.Context,group int,probe func(int)error,retryPermission bool,permissionGrace time.Duration)error{
	if group <= 1 || group > math.MaxInt32 {
		return ErrGroupIdentity
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var permissionUntil time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := probe(group)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			if !retryPermission||!errors.Is(err,syscall.EPERM){return err}
			if permissionUntil.IsZero(){permissionUntil=time.Now().Add(permissionGrace)}
			if !time.Now().Before(permissionUntil){return err}
		}
		// Do not wait beyond the original grace even if the group
		// changes from permission denied to live/observable.
		if !permissionUntil.IsZero()&&!time.Now().Before(permissionUntil){return syscall.EPERM}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
