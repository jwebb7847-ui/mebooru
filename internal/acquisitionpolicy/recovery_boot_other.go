//go:build !darwin

package acquisitionpolicy

import (
	"errors"
	"time"
)

func systemBootTime() (time.Time, error) {
	return time.Time{}, errors.New("this recovery command is available on macOS")
}
