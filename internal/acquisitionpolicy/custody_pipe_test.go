//go:build windows || darwin || linux

package acquisitionpolicy

import(
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestCustodyPipeCancellationWithWriterAlive(t *testing.T){
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close();defer w.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),60*time.Millisecond)
	defer cancel()
	if _,err:=readCustodyByte(ctx,r);!errors.Is(err,context.DeadlineExceeded){t.Fatal("cancel read",err)}
	if _,err:=w.Write([]byte{custodyActivate});err!=nil{t.Fatal("reader closed shared pipe",err)}
	if b,err:=readCustodyByte(context.Background(),r);err!=nil||b!=custodyActivate{t.Fatal("activation",b,err)}
	if err:=w.Close();err!=nil{t.Fatal(err)}
	if _,err:=readCustodyByte(context.Background(),r);err==nil{t.Fatal("writer loss accepted")}
}

func TestCustodyChildExitWithWriterAlive(t *testing.T){
	for _,fail:=range []bool{false,true}{
		dir:=t.TempDir()
		seedDirtyGeneration(t,dir,"a")
		l,err:=LockProfile(dir)
		if err!=nil{t.Fatal(err)}
		rec,err:=l.readFence()
		if err!=nil{t.Fatal(err)}
		r,w,err:=os.Pipe()
		if err!=nil{t.Fatal(err)}
		if _,err:=w.Write([]byte{custodyActivate});err!=nil{t.Fatal(err)}
		done:=make(chan struct{})
		close(done)
		// Keep writer open until RunCustodian has returned; this is the
		// precise Windows shutdown deadlock in the original code.
		err=RunCustodian(context.Background(),dir,rec.generationID(),r,func(context.Context)(CustodyChild,error){
			return custodyLaunchLeaf{done:done,fail:fail},nil
		})
		_ = w.Close()
		if fail{
			if !errors.Is(err,ErrCustodyUnconfirmed){t.Fatal("unconfirmed",err)}
			if err:=l.checkRecovery();!errors.Is(err,ErrWorkerRecoveryRequired){t.Fatal("uncertain stop recovered",err)}
		}else{
			if err!=nil{t.Fatal("confirmed",err)}
			if err:=l.checkRecovery();err!=nil{t.Fatal("cleanup receipt not recognized",err)}
		}
		_ = l.Close()
	}
}
