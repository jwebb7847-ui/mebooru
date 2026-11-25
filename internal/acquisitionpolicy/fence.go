package acquisitionpolicy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
)

var ErrWorkerRecoveryRequired = errors.New("managed acquisition ownership requires recovery")
var ErrStaleWorkerFence = errors.New("stale managed acquisition ownership fence")

// Offset zero is reserved for the Windows byte-range lock. The legacy
// isolated prototype uses version 1 and no generation. Version 2 adds
// a fresh 256-bit generation to each launch. Never unlink or replace
// this inode. Unknown/truncated records fail closed.
const fenceVersion byte = 1
const custodyFenceVersion byte = 2
const fenceClean byte = 'C'
const fenceDirty byte = 'D'
const generationSize = 32

type fenceRecord struct {
	version byte
	state byte
	generation [generationSize]byte
}

func (r fenceRecord) generationID() string {
	return hex.EncodeToString(r.generation[:])
}

// WorkerFence is an in-process single-launch capability. Its generation
// identifies a cleanup receipt, not an authentication secret or PID.
type WorkerFence struct {
	lock *ProfileLock
	generation string
}

func (f *WorkerFence) Generation() string { return f.generation }

// readFence requires l.mu and the open locked descriptor.
func (l *ProfileLock) readFence() (fenceRecord, error) {
	var result fenceRecord
	var buf [3+generationSize+1]byte
	n, err := l.file.ReadAt(buf[:], 0)
	if err != nil && !errors.Is(err, io.EOF) { return result, err }
	if n == 0 && errors.Is(err, io.EOF) {
		result.state = fenceClean
		return result, nil
	}
	if n < 3 || buf[0] != 0 || (buf[2] != fenceClean && buf[2] != fenceDirty) {
		return result, ErrWorkerRecoveryRequired
	}
	result.version, result.state = buf[1], buf[2]
	switch {
	case result.version == fenceVersion && n == 3:
		return result, nil
	case result.version == custodyFenceVersion && n == 3+generationSize:
		copy(result.generation[:], buf[3:n])
		if result.generation == [generationSize]byte{} { return result, ErrWorkerRecoveryRequired }
		return result, nil
	default:
		return result, ErrWorkerRecoveryRequired
	}
}

func (l *ProfileLock) writeAtSync(body []byte, offset int64) error {
	n, err := l.file.WriteAt(body, offset)
	if err != nil { return err }
	if n != len(body) { return io.ErrShortWrite }
	return l.file.Sync()
}

// recoverLocked accepts only a matching terminal receipt for a version 2
// dirty record. A receipt is written by the trusted cleanup custodian
// AFTER it has permanently revoked activation and proven containment
// empty. Missing, stale, malformed or unreadable receipts never clear it.
// No process lookup, wall clock or guessing is used.
func (l *ProfileLock) recoverLocked() error {
	if l.file == nil || l.root == nil || l.activeFence != nil { return ErrWorkerRecoveryRequired }
	record, err := l.readFence()
	if err != nil { return err }
	if record.state == fenceClean { return nil }
	if record.version != custodyFenceVersion { return ErrWorkerRecoveryRequired }
	if err := verifyCleanupReceipt(l.root, record.generationID()); err != nil {
		return errors.Join(ErrWorkerRecoveryRequired, err)
	}
	return l.writeAtSync([]byte{fenceClean}, 2)
}

func (l *ProfileLock) checkRecovery() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recoverLocked()
}

// BeginWorker persists the new generation before permitting ANY child
// launch. A partial/failed write never authorizes launch. A successfully
// synced random generation makes every previous receipt stale.
func (l *ProfileLock) BeginWorker() (*WorkerFence, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.recoverLocked(); err != nil { return nil, err }
	record := fenceRecord{version: custodyFenceVersion, state: fenceDirty}
	if _,err := rand.Read(record.generation[:]); err != nil { return nil, err }
	if record.generation == [generationSize]byte{} { return nil, ErrWorkerRecoveryRequired }
	body := append([]byte{record.version, record.state}, record.generation[:]...)
	if err := l.writeAtSync(body, 1); err != nil { return nil, err }
	f := &WorkerFence{lock:l, generation:record.generationID()}
	l.activeFence = f
	return f, nil
}

// ConfirmStopped is for an in-process supervisor that has independent,
// truthful no-child/containment proof. Never call merely on EOF, timeout
// or parent exit. Failed persistence retains ownership; stale capabilities
// cannot clear a subsequent launch.
func (f *WorkerFence) ConfirmStopped() error {
	if f == nil || f.lock == nil { return ErrStaleWorkerFence }
	l := f.lock
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil || l.activeFence != f { return ErrStaleWorkerFence }
	record,err := l.readFence()
	if err != nil { return err }
	if record.version != custodyFenceVersion || record.generationID() != f.generation { return ErrWorkerRecoveryRequired }
	if err := l.writeAtSync([]byte{fenceClean}, 2); err != nil { return err }
	l.activeFence = nil
	return nil
}
