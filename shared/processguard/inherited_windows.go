//go:build windows

package processguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Duplicate only the two explicitly selected handles, with inheritance
// enabled for the short launch window. Go's Windows exec handle list
// restricts inheritance to stdio and these handles; do not combine this
// with arbitrary SysProcAttr or untrusted launchers.
func attachInheritedPipes(cmd *exec.Cmd,r,w *os.File)(string,func(),error){
	return attachWindowsPipes(cmd,r,w)
}

func attachWindowsPipes(cmd *exec.Cmd,files ...*os.File)(string,func(),error){
	handles:=make([]windows.Handle,len(files))
	release:=func(){
		for i,h:=range handles{
			if h!=0{_ = windows.CloseHandle(h);handles[i]=0}
		}
	}
	for i,f:=range files{
		err:=windows.DuplicateHandle(windows.CurrentProcess(),windows.Handle(f.Fd()),
			windows.CurrentProcess(),&handles[i],0,true,windows.DUPLICATE_SAME_ACCESS)
		runtime.KeepAlive(f)
		if err!=nil{release();return "",nil,err}
	}
	inherited:=make([]syscall.Handle,len(handles))
	token:="handle"
	for i,h:=range handles{inherited[i]=syscall.Handle(h);token+=fmt.Sprintf(":%d",uintptr(h))}
	cmd.SysProcAttr=&syscall.SysProcAttr{AdditionalInheritedHandles:inherited}
	return token,release,nil
}

func openInheritedPipes(token string)(*os.File,*os.File,error){
	files,err:=openWindowsPipes(token,2)
	if err!=nil{return nil,nil,err}
	return files[0],files[1],nil
}

// OpenJobBrokerPipes adopts exactly four explicitly inherited ends:
// library protocol read/write followed by custody broker read/write.
func OpenJobBrokerPipes(token string)([]*os.File,error){return openWindowsPipes(token,4)}

func openWindowsPipes(token string,count int)([]*os.File,error){
	parts:=strings.Split(token,":")
	if len(parts)!=count+1||parts[0]!="handle"{return nil,ErrInheritedPipes}
	handles:=make([]windows.Handle,count)
	seen:=make(map[windows.Handle]bool)
	for i,s:=range parts[1:]{
		n,err:=strconv.ParseUint(s,10,strconv.IntSize)
		if err!=nil||n==0||strconv.FormatUint(n,10)!=s{return nil,ErrInheritedPipes}
		h:=windows.Handle(n)
		if h==windows.InvalidHandle||uintptr(h)==os.Stdin.Fd()||uintptr(h)==os.Stdout.Fd()||uintptr(h)==os.Stderr.Fd(){
			return nil,ErrInheritedPipes
		}
		kind,err:=windows.GetFileType(h)
		if err!=nil||kind!=windows.FILE_TYPE_PIPE{return nil,errors.Join(ErrInheritedPipes,err)}
		if seen[h]{return nil,ErrInheritedPipes}
		seen[h]=true
		handles[i]=h
	}
	// The inherited handles must not escape to a later downloader.
	for _,h:=range handles{
		if err:=windows.SetHandleInformation(h,windows.HANDLE_FLAG_INHERIT,0);err!=nil{return nil,err}
	}
	files:=make([]*os.File,0,count)
	for i,h:=range handles{
		f:=os.NewFile(uintptr(h),"private-pipe")
		if f==nil{
			for _,opened:=range files{_ = opened.Close()}
			for _,remaining:=range handles[i:]{_ = windows.CloseHandle(remaining)}
			return nil,ErrInheritedPipes
		}
		files=append(files,f)
	}
	return files,nil
}
