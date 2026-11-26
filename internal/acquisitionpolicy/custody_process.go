package acquisitionpolicy

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// CustodyProcess is the library-side owner of a trusted one-shot cleanup
// process. The process must survive library cancellation/exit to perform
// cleanup, so it must not use a library-owned CommandContext. Its stdin
// is exclusively activation/liveness; stdout is readiness only. Worker
// IPC uses separate inherited channels (not implemented by this type).
type CustodyProcess struct{
	mu sync.Mutex
	fence *WorkerFence
	life io.WriteCloser
	done chan struct{}
	waitErr error
	ready bool
	attempted bool
	revoked bool
	stopped bool
}

// StartCustodyProcess starts the dormant custodian, not a worker. The
// caller constructs the known bundled executable entry and passes this
// exact fence generation, never user/remote command arguments. Returns
// a nonnil owner on post-start error so caller can reap the custodian.
func StartCustodyProcess(ctx context.Context,cmd *exec.Cmd,fence *WorkerFence)(*CustodyProcess,error){
	if ctx==nil||cmd==nil||fence==nil||!filepath.IsAbs(cmd.Path)||cmd.Process!=nil||
		cmd.Cancel!=nil||cmd.Stdin!=nil||cmd.Stdout!=nil{return nil,ErrCustodyActivation}
	if err:=ctx.Err();err!=nil{return nil,err}
	if err:=fence.claimCustody();err!=nil{return nil,err}
	life,err:=cmd.StdinPipe()
	if err!=nil{return nil,err}
	r,w,err:=os.Pipe()
	if err!=nil{_ = life.Close();return nil,err}
	defer r.Close()
	defer w.Close()
	cmd.Stdout=w
	if err:=cmd.Start();err!=nil{_ = life.Close();return nil,err}
	_ = w.Close()
	p:=&CustodyProcess{fence:fence,life:life,done:make(chan struct{})}
	go func(){p.waitErr=cmd.Wait();close(p.done)}()
	type result struct{byteValue byte;err error}
	ch:=make(chan result,1)
	go func(){
		var b [1]byte
		_,err:=io.ReadFull(r,b[:])
		ch<-result{b[0],err}
	}()
	select{
	case got:=<-ch:
		if got.err==nil&&got.byteValue==custodyReady{
			select{case <-p.done:default:p.ready=true;return p,nil}
		}
		p.revoke()
		return p,errors.Join(ErrCustodyActivation,got.err)
	case <-p.done:
		p.revoke()
		return p,errors.Join(ErrCustodyActivation,p.waitErr)
	case <-ctx.Done():
		p.revoke()
		return p,ctx.Err()
	}
}

func(p *CustodyProcess)Done()<-chan struct{}{return p.done}

// Activate syncs the armed generation BEFORE writing the only activation
// byte. Any attempted delivery is treated as uncertain until receipt.
// No method exposes the liveness writer to ordinary application code.
func(p *CustodyProcess)Activate()error{
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready||p.attempted||p.revoked{return ErrCustodyActivation}
	select{case <-p.done:p.revoke();return ErrCustodyActivation;default:}
	if err:=p.fence.arm(true);err!=nil{p.revoke();return err}
	p.attempted=true
	n,err:=p.life.Write([]byte{custodyActivate})
	if n!=1||err!=nil{p.revoke();return errors.Join(ErrCustodyActivation,err)}
	return nil
}

// Caller holds mu, or p is not yet published.
func(p *CustodyProcess)revoke(){
	p.revoked=true
	if p.life!=nil{_ = p.life.Close();p.life=nil}
}

// Stop never kills the cleanup process on timeout. Close the parent
// liveness channel, await direct custodian exit, and require its exact
// terminal receipt if activation was attempted. A nonzero exit is
// diagnostic; receipt plus exit is proof under the trusted contract.
// The manager, not this process, clears the live ownership fence.
func(p *CustodyProcess)Stop(ctx context.Context)error{
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped{return nil}
	p.revoke()
	select{case <-p.done:case <-ctx.Done():return ctx.Err()}
	if p.attempted{
		f:=p.fence
		l:=f.lock
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.file==nil||l.root==nil||l.activeFence!=f{return ErrWorkerRecoveryRequired}
		if err:=verifyCleanupReceipt(l.root,f.Generation());err!=nil{
			return errors.Join(ErrWorkerRecoveryRequired,err)
		}
	}
	p.stopped=true
	return nil
}

func(p *CustodyProcess)ExitError(ctx context.Context)error{
	select{case <-p.done:return p.waitErr;case <-ctx.Done():return ctx.Err()}
}
