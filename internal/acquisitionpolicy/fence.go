package acquisitionpolicy

import (
	"errors"
	"io"
)

// ErrWorkerRecoveryRequired means prior worker ownership has not been
// proven clean. Acquiring the advisory lock alone is not recovery proof.
var ErrWorkerRecoveryRequired = errors.New("managed acquisition ownership requires recovery")
var ErrStaleWorkerFence = errors.New("stale managed acquisition ownership fence")

// The first byte is reserved for Windows' byte-range lock. Store version
// and state at offsets 1 and 2 in the SAME persistent, locked inode.
// Never truncate, unlink, or replace the file to update its state.
// Empty is the legacy/pre-launch state; only exact records are accepted.
const fenceVersion byte = 1
const fenceClean byte = 'C'
const fenceDirty byte = 'D'

// WorkerFence is a single-launch capability, not evidence of process death.
// A future contained launcher must acquire it BEFORE starting ANY child,
// and clear it only after verifying that no owned processes remain.
// There is intentionally no automatic reset of a dirty reopened record.
type WorkerFence struct {
	lock *ProfileLock
}

// readFence requires l.mu and an open locked descriptor.
func (l *ProfileLock) readFence() (bool, error) {
	var buf [4]byte
	n, err := l.file.ReadAt(buf[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if n == 0 && errors.Is(err, io.EOF) {
		return true, nil
	}
	if n != 3 || buf[0] != 0 || buf[1] != fenceVersion {
		return false, ErrWorkerRecoveryRequired
	}
	switch buf[2] {
	case fenceClean:
		return true, nil
	case fenceDirty:
		return false, nil
	default:
		return false, ErrWorkerRecoveryRequired
	}
}

// writeFence requires l.mu. Sync before returning success; a failed or
// interrupted write never authorizes launch. A process crash (not a
// hostile filesystem rollback) cannot erase a successfully synced fence.
func (l *ProfileLock) writeFence(state byte) error {
	record := []byte{fenceVersion, state}
	n, err := l.file.WriteAt(record, 1)
	if err != nil {
		return err
	}
	if n != len(record) {
		return io.ErrShortWrite
	}
	return l.file.Sync()
}

// BeginWorker refuses both a second in-process launch and a recovered
// profile with unconfirmed old ownership. The caller may start a child
// only after this method succeeds. All worker launch paths must opt into
// this contract; the legacy standalone path has not been migrated.
func (l *ProfileLock) BeginWorker() (*WorkerFence, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil || l.activeFence != nil {
		return nil, ErrWorkerRecoveryRequired
	}
	clean, err := l.readFence()
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, ErrWorkerRecoveryRequired
	}
	if err := l.writeFence(fenceDirty); err != nil {
		return nil, err
	}
	f := &WorkerFence{lock: l}
	l.activeFence = f
	return f, nil
}

// ConfirmStopped may be called only after the contained launcher proves
// that startup created no processes or that ALL owned processes stopped.
// It must not be called merely on session EOF, a timeout, or parent exit.
// Failed persistence retains the capability and prevents lock release.
// A stale capability cannot clear a later launch's marker.
func (f *WorkerFence) ConfirmStopped() error {
	if f == nil || f.lock == nil {
		return ErrStaleWorkerFence
	}
	l := f.lock
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil || l.activeFence != f {
		return ErrStaleWorkerFence
	}
	if err := l.writeFence(fenceClean); err != nil {
		return err
	}
	l.activeFence = nil
	return nil
}
