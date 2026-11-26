package processguard

import (
	"errors"
	"os"
	"os/exec"
)

var ErrInheritedPipes = errors.New("invalid private inherited pipe configuration")

// AttachInheritedPipes passes exactly two anonymous pipe ends alongside
// ordinary stdio. It is reserved for trusted internal bootstrap commands,
// not arbitrary external plugin or protocol input. A command can be
// configured only once. The caller retains ownership of read and write,
// and MUST call release after the Start attempt (also on error). It
// must close its copies of the passed ends immediately after startup.
// The returned token identifies inherited descriptors/handles, not a
// credential, address or path. No socket or filesystem rendezvous.
func AttachInheritedPipes(cmd *exec.Cmd, read, write *os.File) (token string, release func(), err error) {
	if cmd == nil || cmd.Process != nil || cmd.SysProcAttr != nil || len(cmd.ExtraFiles) != 0 ||
		read == nil || write == nil || read.Fd() == write.Fd() {
		return "", nil, ErrInheritedPipes
	}
	for _, f := range []*os.File{read,write} {
		if info,err:=f.Stat();err!=nil||info.Mode()&os.ModeNamedPipe==0{
			return "",nil,errors.Join(ErrInheritedPipes,err)
		}
	}
	return attachInheritedPipes(cmd,read,write)
}

// OpenInheritedPipes is called exactly once by the trusted child entry
// before any application startup or subprocess creation. It adopts the
// two passed ends and makes them non-inheritable for subsequent execs.
// No remote caller may supply the bootstrap token.
func OpenInheritedPipes(token string)(read,write *os.File,err error){
	return openInheritedPipes(token)
}
