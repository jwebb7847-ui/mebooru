//go:build darwin || linux

package acquisitionpolicy

import(
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Only read after poll indicates data or writer loss. Single reader,
// bounded wait even for inherited blocking descriptors.
func readCustodyByte(ctx context.Context,f *os.File)(byte,error){
	if f==nil{return 0,ErrCustodyActivation}
	fd:=[]unix.PollFd{{Fd:int32(f.Fd()),Events:unix.POLLIN}}
	for{
		if err:=ctx.Err();err!=nil{return 0,err}
		fd[0].Revents=0
		n,err:=unix.Poll(fd,int(custodyPipeInterval.Milliseconds()))
		if errors.Is(err,unix.EINTR){continue}
		if err!=nil{return 0,err}
		if n==0{continue}
		if fd[0].Revents&unix.POLLIN!=0{
			var b [1]byte
			_,err:=io.ReadFull(f,b[:])
			if err!=nil{return 0,err}
			if err:=ctx.Err();err!=nil{return 0,err}
			return b[0],nil
		}
		if fd[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL)!=0{return 0,io.EOF}
		return 0,ErrCustodyActivation
	}
}
