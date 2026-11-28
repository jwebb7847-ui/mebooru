package acquisitionpolicy

import (
	"time"

	"golang.org/x/sys/unix"
)

func systemBootTime() (time.Time, error) {
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(boot.Sec, int64(boot.Usec)*1000), nil
}
