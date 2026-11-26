package acquisitionpolicy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// CustodySessionPeer connects the library directly to the contained
// worker over inherited pipes. Custodian stdin/stdout remain independent
// activation/liveness/readiness. On any post-start error it is a cleanup
// handle even if no session was established.
type CustodySessionPeer struct {
	owner *CustodyProcess
	session *workerproto.Session
	control *SessionPeer
	stopMu sync.Mutex
	shutdownAttempted bool
	shutdownDiagnostic error
}

// StartCustodySession accepts a trusted dormant-custodian command,
// constructed by internal bootstrap code with the exact fence generation.
// It appends the inherited protocol pipe token as the final argument.
// No application caller yet: bundled identity/profile validation and real
// worker startup/drain/containment remain production requirements.
func StartCustodySession(startup,lifetime context.Context,cmd *exec.Cmd,fence *WorkerFence,handler workerproto.Handler)(*CustodySessionPeer,error){
	if startup==nil||lifetime==nil||cmd==nil||fence==nil{return nil,ErrCustodyActivation}
	if err:=lifetime.Err();err!=nil{return nil,err}
	startCtx,stopStart:=context.WithTimeout(startup,10*time.Second)
	defer stopStart()
	stopOnLifetime:=context.AfterFunc(lifetime,stopStart)
	defer stopOnLifetime()
	childRead,parentWrite,err:=os.Pipe()
	if err!=nil{return nil,err}
	defer childRead.Close()
	parentRead,childWrite,err:=os.Pipe()
	if err!=nil{_ = parentWrite.Close();return nil,err}
	defer childWrite.Close()
	taken:=false
	defer func(){if !taken{_ = parentRead.Close();_ = parentWrite.Close()}}()
	token,release,err:=processguard.AttachInheritedPipes(cmd,childRead,childWrite)
	if err!=nil{return nil,err}
	cmd.Args=append(cmd.Args,token)
	owner,err:=StartCustodyProcess(startCtx,cmd,fence)
	release()
	_ = childRead.Close()
	_ = childWrite.Close()
	if owner==nil{return nil,err}
	p:=&CustodySessionPeer{owner:owner}
	if err!=nil{return p,err}
	// Revoke liveness independently of protocol I/O. This also unblocks
	// a stalled handshake when a platform's pipe Close alone cannot.
	sessionCtx,cancel:=context.WithCancel(lifetime)
	revokeStartup:=context.AfterFunc(startCtx,func(){cancel();owner.Revoke()})
	defer revokeStartup()
	if err:=startCtx.Err();err!=nil{cancel();owner.Revoke();return p,err}
	if err:=owner.Activate();err!=nil{cancel();owner.Revoke();return p,err}
	go func(){
		select{
		case <-owner.Done():cancel()
		case <-sessionCtx.Done():owner.Revoke()
		}
	}()
	session,err:=workerproto.OpenSession(sessionCtx,parentRead,parentWrite,workerproto.Library,handler)
	taken=true // OpenSession owns both ends even on failure.
	if err!=nil||!revokeStartup()||startCtx.Err()!=nil||sessionCtx.Err()!=nil{
		cancel()
		owner.Revoke()
		if session!=nil{session.Close()}
		return p,errors.Join(ErrCustodyActivation,err,startCtx.Err(),sessionCtx.Err())
	}
	p.session=session
	p.control,_=NewSessionPeer(session,p.Stop)
	go func(){<-session.Done();cancel();owner.Revoke()}()
	return p,nil
}

func(p *CustodySessionPeer)Done()<-chan struct{}{
	if p.session!=nil{return p.session.Done()}
	return p.owner.Done()
}

// Session returns the internal typed-operation transport. It is nil for
// a failed startup's cleanup-only handle, which must never be attached.
func(p *CustodySessionPeer)Session()*workerproto.Session{return p.session}

func(p *CustodySessionPeer)ApplyOffline(ctx context.Context,offline bool)error{
	if p.control==nil{return ErrCustodyActivation}
	return p.control.ApplyOffline(ctx,offline)
}
func(p *CustodySessionPeer)ReserveIdle(ctx context.Context)(bool,error){
	if p.control==nil{return false,ErrCustodyActivation}
	return p.control.ReserveIdle(ctx)
}

// Stop attempts bounded graceful shutdown once; shutdown response or
// EOF is not containment proof. Even on refusal, close the session and
// revoke custody, then require the matching receipt. Retry only cleanup
// verification on failure. Report graceful-stop diagnostics separately.
func(p *CustodySessionPeer)Stop(ctx context.Context)error{
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	if !p.shutdownAttempted{
		p.shutdownAttempted=true
		if p.session!=nil{
			deadline,cancel:=context.WithTimeout(ctx,2*time.Second)
			e,err:=p.session.Call(deadline,workerproto.Shutdown,nil)
			cancel()
			if err==nil&&e.Error!=nil{err=ErrControlRefused}
			p.shutdownDiagnostic=err
			p.owner.Revoke()
			p.session.Close()
		}
	}
	return p.owner.Stop(ctx)
}

func(p *CustodySessionPeer)ShutdownDiagnostic()error{
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	return p.shutdownDiagnostic
}
