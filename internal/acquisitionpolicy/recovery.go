package acquisitionpolicy

import (
	"errors"
	"time"
)

var errRebootRequired = errors.New("restart the Mac before recovering this managed acquisition profile")

// RecoverAfterReboot is an explicit offline repair for a valid ownership
// record whose custodian could no longer produce a receipt. A new Mac boot
// and exclusive ownership prove the previous generation cannot still run.
func RecoverAfterReboot(dir string) error {
	boot, err := systemBootTime()
	if err != nil {
		return err
	}
	return recoverAfterReboot(dir, boot)
}

func recoverAfterReboot(dir string, boot time.Time) (result error) {
	lock, err := LockProfile(dir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	info, err := lock.file.Stat()
	if err != nil {
		return err
	}
	if boot.IsZero() || !boot.After(info.ModTime()) {
		return errRebootRequired
	}
	record, err := lock.readFence()
	if err != nil {
		return err
	}
	if record.version != custodyFenceVersion {
		return ErrWorkerRecoveryRequired
	}
	store, err := Open(dir)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Save(true); err != nil {
		return err
	}
	// Keep the original lock inode and stale receipt for diagnosis.
	return lock.writeAtSync([]byte{fenceClean}, 2)
}
