//go:build !windows

package acquisitionstage

import(
	"os"
	"syscall"
)

func singleLink(_ *os.File,info os.FileInfo)bool{
	s,ok:=info.Sys().(*syscall.Stat_t)
	return ok&&s.Nlink==1
}
