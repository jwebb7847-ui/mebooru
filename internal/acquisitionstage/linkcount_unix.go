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

func sameVolume(a,b *os.File)bool{
	x,err:=a.Stat()
	if err!=nil{return false}
	y,err:=b.Stat()
	if err!=nil{return false}
	s,ok:=x.Sys().(*syscall.Stat_t)
	t,other:=y.Sys().(*syscall.Stat_t)
	return ok&&other&&s.Dev==t.Dev
}
