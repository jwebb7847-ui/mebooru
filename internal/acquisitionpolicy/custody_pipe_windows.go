//go:build windows

package acquisitionpolicy

import (
	"context"
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var custodyPeekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

// Windows anonymous pipes are synchronous. A concurrent Close can wait
// forever for a blocked ReadFile while the parent writer is alive.
// Peek without consuming data; only this reader can consume the byte.
// EOF/broken pipe or any unexpected failure revokes liveness.
func readCustodyByte(ctx context.Context,f *os.File)(byte,error){
	if f==nil{return 0,ErrCustodyActivation}
	ticker:=time.NewTicker(custodyPipeInterval)
	defer ticker.Stop()
	for{
		if err:=ctx.Err();err!=nil{return 0,err}
		var available uint32
		ok,_,err:=custodyPeekNamedPipe.Call(f.Fd(),0,0,0,uintptr(unsafe.Pointer(&available)),0)
		if ok==0{return 0,err}
		if available>0{
			var b [1]byte
			_,err:=io.ReadFull(f,b[:])
			if err!=nil{return 0,err}
			if err:=ctx.Err();err!=nil{return 0,err}
			return b[0],nil
		}
		select{case <-ctx.Done():return 0,ctx.Err();case <-ticker.C:}
	}
}
