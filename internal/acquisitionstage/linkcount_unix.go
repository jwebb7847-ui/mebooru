//go:build !windows

package acquisitionstage

import(
	"os"
	"syscall"
	"fmt"
)

func singleLink(f *os.File,info os.FileInfo)bool{return exactLinks(f,info,1)}
func exactLinks(_ *os.File,info os.FileInfo,n uint64)bool{
	s,ok:=info.Sys().(*syscall.Stat_t)
	return ok&&uint64(s.Nlink)==n
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

func diskFileIdentity(_ *os.File,info os.FileInfo)(string,bool){
	s,ok:=info.Sys().(*syscall.Stat_t)
	if !ok||s.Ino==0{return "",false}
	return fmt.Sprintf("unix:%x:%x",s.Dev,s.Ino),true
}
