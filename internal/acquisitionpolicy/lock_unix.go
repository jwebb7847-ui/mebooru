//go:build darwin || linux

package acquisitionpolicy

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLock(f *os.File)error{
	err:=unix.Flock(int(f.Fd()),unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err,unix.EWOULDBLOCK)||errors.Is(err,unix.EAGAIN){return ErrProfileOwned}
	return err
}
