//go:build !darwin && !linux && !windows

package acquisitionpolicy

import ("errors";"os")

func tryLock(*os.File)error{return errors.New("managed profile ownership unsupported on this platform")}
