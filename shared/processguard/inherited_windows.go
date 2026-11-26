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
	var handles [2]windows.Handle
	release:=func(){
		for i,h:=range handles{
			if h!=0{_ = windows.CloseHandle(h);handles[i]=0}
		}
	}
	for i,f:=range []*os.File{r,w}{
		err:=windows.DuplicateHandle(windows.CurrentProcess(),windows.Handle(f.Fd()),
			windows.CurrentProcess(),&handles[i],0,true,windows.DUPLICATE_SAME_ACCESS)
		runtime.KeepAlive(f)
		if err!=nil{release();return "",nil,err}
	}
	cmd.SysProcAttr=&syscall.SysProcAttr{AdditionalInheritedHandles:[]syscall.Handle{
		syscall.Handle(handles[0]),syscall.Handle(handles[1]),
	}}
	token:=fmt.Sprintf("handle:%d:%d",uintptr(handles[0]),uintptr(handles[1]))
	return token,release,nil
}

func openInheritedPipes(token string)(*os.File,*os.File,error){
	parts:=strings.Split(token,":")
	if len(parts)!=3||parts[0]!="handle"{return nil,nil,ErrInheritedPipes}
	var handles [2]windows.Handle
	for i,s:=range parts[1:]{
		n,err:=strconv.ParseUint(s,10,strconv.IntSize)
		if err!=nil||n==0||strconv.FormatUint(n,10)!=s{return nil,nil,ErrInheritedPipes}
		h:=windows.Handle(n)
		if h==windows.InvalidHandle||uintptr(h)==os.Stdin.Fd()||uintptr(h)==os.Stdout.Fd()||uintptr(h)==os.Stderr.Fd(){
			return nil,nil,ErrInheritedPipes
		}
		kind,err:=windows.GetFileType(h)
		if err!=nil||kind!=windows.FILE_TYPE_PIPE{return nil,nil,errors.Join(ErrInheritedPipes,err)}
		handles[i]=h
	}
	if handles[0]==handles[1]{return nil,nil,ErrInheritedPipes}
	// The inherited handles must not escape to a later downloader.
	for _,h:=range handles{
		if err:=windows.SetHandleInformation(h,windows.HANDLE_FLAG_INHERIT,0);err!=nil{return nil,nil,err}
	}
	r,w:=os.NewFile(uintptr(handles[0]),"private-input"),os.NewFile(uintptr(handles[1]),"private-output")
	if r==nil||w==nil{
		if r!=nil{_ = r.Close()}else{_ = windows.CloseHandle(handles[0])}
		if w!=nil{_ = w.Close()}else{_ = windows.CloseHandle(handles[1])}
		return nil,nil,ErrInheritedPipes
	}
	return r,w,nil
}
