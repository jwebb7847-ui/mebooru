//go:build !windows && !darwin && !linux

package acquisitionpolicy

import("context";"os")

func readCustodyByte(context.Context,*os.File)(byte,error){return 0,ErrCustodyActivation}
