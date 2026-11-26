package main

import(
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

// Both primary and broker completion are required for a receipt.
type probeCustody struct{
	primary acquisitionpolicy.CustodyChild
	set *processguard.OperationSet
	stopBroker context.CancelFunc
	brokerDone chan struct{}
	brokerErr error
	done chan struct{}
}
func(p *probeCustody)Done()<-chan struct{}{return p.done}
func(p *probeCustody)Stop(ctx context.Context)error{
	p.stopBroker()
	combined,err:=acquisitionpolicy.NewCombinedCustody(p.primary,p.set)
	if err!=nil{return err}
	err=combined.Stop(ctx)
	select{
	case <-p.brokerDone:return errors.Join(err,p.brokerErr)
	case <-ctx.Done():return errors.Join(err,ctx.Err())
	}
}

// Fixed leaf only: no command, path or network parameters from IPC.
func operationProbeCommand(mode string)(*exec.Cmd,error){
	path,err:=os.Executable()
	if err!=nil{return nil,err}
	path,err=filepath.EvalSymlinks(path)
	if err!=nil{return nil,err}
	return exec.Command(path,mode),nil
}
type readyOperationProbe struct{
	processguard.OperationChild
	ready *os.File
}
func(p *readyOperationProbe)Activate()error{
	if err:=p.OperationChild.Activate();err!=nil{return err}
	result:=make(chan error,1)
	go func(){var b [1]byte;_,err:=io.ReadFull(p.ready,b[:]);if err==nil&&b[0]!=7{err=processguard.ErrProbeExecutable};result<-err}()
	select{
	case err:=<-result:_ = p.ready.Close();return err
	case <-time.After(5*time.Second):return context.DeadlineExceeded
	}
}
func(p *readyOperationProbe)Stop(ctx context.Context)error{
	err:=p.OperationChild.Stop(ctx)
	_ = p.ready.Close()
	return err
}
func startOperationProbe(ctx context.Context)(processguard.OperationChild,error){
	if err:=ctx.Err();err!=nil{return nil,err}
	r,w,err:=os.Pipe()
	if err!=nil{return nil,err}
	defer w.Close()
	child,err:=startOperationProbePlatform(w)
	if child==nil{_ = r.Close();return nil,err}
	return &readyOperationProbe{OperationChild:child,ready:r},err
}
func operationProbeLeaf()error{
	if _,err:=os.Stdout.Write([]byte{7});err!=nil{return err}
	done:=make(chan struct{})
	go func(){var b [1]byte;_,_=os.Stdin.Read(b[:]);close(done)}()
	select{case <-done:return nil;case <-time.After(40*time.Second):return context.DeadlineExceeded}
}
