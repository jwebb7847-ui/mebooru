//go:build windows

package processguard

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

const jobActivationByte byte = 1
var ErrJobActivation = errors.New("managed Windows worker activation refused")

// AwaitJobActivation is the mandatory first read of the trusted dormant
// Windows worker entry. No app initialization, network, or descendant
// creation may happen before this returns nil. Subsequent input is IPC.
func AwaitJobActivation(in io.Reader) error {
	var b [1]byte
	n,err:=io.ReadFull(in,b[:])
	if err!=nil||n!=1||b[0]!=jobActivationByte{return errors.Join(ErrJobActivation,err)}
	return nil
}

// DormantJob owns the private Job Object and the original direct child.
// A failed assignment/activation retains cleanup ownership. No PID
// lookup, process-name matching, or arbitrary process termination.
type DormantJob struct {
	mu sync.Mutex
	job *Job
	cmd *exec.Cmd
	input io.WriteCloser
	assigned bool
	separateProtocol bool
	activated bool
	revoked bool
	stopped bool
	done chan struct{}
	waitErr error
}

// StartDormantJob requires an absolute, trusted dormant worker entry.
// stdin is reserved for activation then protocol; stdout remains caller
// supplied. Do not use parent cancellation to kill the cleanup custodian.
// The returned nonnil child on error MUST still be stopped by its owner.
func StartDormantJob(cmd *exec.Cmd)(*DormantJob,error){
	return startDormantJob(cmd,false)
}

// StartDormantJobWithPipes gives the worker separate protocol handles.
// stdin remains activation-only. The trusted private entry receives
// the inherited pipe token as the final argument and must adopt it
// only after AwaitJobActivation. The caller owns and closes r/w.
func StartDormantJobWithPipes(cmd *exec.Cmd,r,w *os.File)(*DormantJob,error){
	if cmd==nil||cmd.Stdin!=nil{return nil,ErrJobActivation}
	token,release,err:=AttachInheritedPipes(cmd,r,w)
	if err!=nil{return nil,err}
	defer release()
	cmd.Args=append(cmd.Args,token)
	return startDormantJob(cmd,true)
}

func startDormantJob(cmd *exec.Cmd,separate bool)(*DormantJob,error){
	if cmd==nil||!filepath.IsAbs(cmd.Path)||cmd.Process!=nil||(!separate&&cmd.SysProcAttr!=nil)||cmd.Stdin!=nil||len(cmd.ExtraFiles)!=0{return nil,ErrJobActivation}
	job,err:=NewJob()
	if err!=nil{return nil,err}
	input,err:=cmd.StdinPipe()
	if err!=nil{return nil,errors.Join(err,job.Close())}
	if err:=cmd.Start();err!=nil{
		_ = input.Close()
		return nil,errors.Join(err,job.Close())
	}
	d:=&DormantJob{job:job,cmd:cmd,input:input,separateProtocol:separate,done:make(chan struct{})}
	assignErr:=job.Assign(cmd.Process)
	d.assigned=assignErr==nil
	go func(){d.waitErr=cmd.Wait();close(d.done)}()
	if assignErr!=nil{
		d.revoked=true
		_ = input.Close()
		return d,assignErr
	}
	return d,nil
}

func(d *DormantJob)Done()<-chan struct{}{return d.done}

// Activate sends the one-byte arm marker only after verified assignment.
// An uncertain write revokes future activation; Stop still proves cleanup.
func(d *DormantJob)Activate()error{
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.assigned||d.activated||d.revoked{return ErrJobActivation}
	select{case <-d.done:return ErrJobActivation;default:}
	d.activated=true
	n,err:=d.input.Write([]byte{jobActivationByte})
	if n!=1||err!=nil{
		d.revoked=true
		_ = d.input.Close()
		return errors.Join(ErrJobActivation,err)
	}
	return nil
}

// ProtocolInput is available only after the activation marker has been
// written. The trusted IPC owner may send protocol bytes, never another
// activation. Stop owns closing this stream.
func(d *DormantJob)ProtocolInput()(io.WriteCloser,error){
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.activated||d.revoked||d.separateProtocol{return nil,ErrJobActivation}
	return d.input,nil
}

// Stop seals the launch, closes input and proves zero Job membership
// and direct-child reaping. An assignment failure has no authorized
// children, but the original dormant direct child must still be reaped.
// Exit diagnostics do not substitute for containment proof.
func(d *DormantJob)Stop(ctx context.Context)error{
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped{return nil}
	d.revoked=true
	_ = d.input.Close()
	if !d.assigned{
		select{
		case <-d.done:
		default:
			if err:=d.cmd.Process.Kill();err!=nil&&!errors.Is(err,os.ErrProcessDone){
				select{case <-d.done:default:return err}
			}
		}
	}
	if err:=d.job.TerminateAndWait(ctx);err!=nil{return err}
	select{
	case <-d.done:
	case <-ctx.Done():return ctx.Err()
	}
	if err:=d.job.Close();err!=nil{return err}
	d.stopped=true
	return nil
}

func(d *DormantJob)ExitError(ctx context.Context)error{
	select{case <-d.done:return d.waitErr;case <-ctx.Done():return ctx.Err()}
}
