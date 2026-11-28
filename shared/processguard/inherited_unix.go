//go:build darwin || linux

package processguard

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

const inheritedPipeToken = "fd:3:4"

func attachInheritedPipes(cmd *exec.Cmd,r,w *os.File)(string,func(),error){
	cmd.ExtraFiles=[]*os.File{r,w}
	return inheritedPipeToken,func(){},nil
}

func openInheritedPipes(token string)(*os.File,*os.File,error){
	if token!=inheritedPipeToken{return nil,nil,ErrInheritedPipes}
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	r,w:=os.NewFile(3,"private-input"),os.NewFile(4,"private-output")
	for _,f:=range []*os.File{r,w}{
		info,err:=f.Stat()
		if err!=nil||info.Mode()&os.ModeNamedPipe==0{
			_ = r.Close()
			_ = w.Close()
			return nil,nil,errors.Join(ErrInheritedPipes,err)
		}
	}
	return r,w,nil
}
