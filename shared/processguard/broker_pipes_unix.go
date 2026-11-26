//go:build darwin || linux

package processguard

import("os";"syscall")

// AdoptGuardBrokerPipes is used only in the trusted guard/worker entry.
// These descriptors are distinct from fd 3's liveness and ordinary stdio.
func AdoptGuardBrokerPipes()(*os.File,*os.File,error){
	syscall.CloseOnExec(4)
	syscall.CloseOnExec(5)
	r,w:=os.NewFile(4,"broker-input"),os.NewFile(5,"broker-output")
	if err:=validateProbePipes(r,w);err!=nil{_ = r.Close();_ = w.Close();return nil,nil,err}
	return r,w,nil
}
