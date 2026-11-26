//go:build !darwin && !linux && !windows

package processguard

import("os";"os/exec")

func attachInheritedPipes(*exec.Cmd,*os.File,*os.File)(string,func(),error){
	return "",nil,ErrInheritedPipes
}
func openInheritedPipes(string)(*os.File,*os.File,error){
	return nil,nil,ErrInheritedPipes
}
