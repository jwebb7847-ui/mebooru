package acquisitionpolicy

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

const custodyActivate byte = 1
const custodyReady byte = 2
const custodyStartupTimeout = 10*time.Second
const custodyStopTimeout = 15*time.Second

var ErrCustodyActivation = errors.New("managed cleanup custody activation refused")
var ErrCustodyUnconfirmed = errors.New("managed cleanup custody cannot confirm process ownership")

// CustodyChild is a trusted, contained acquisition launch. Done is
// not containment proof. Stop returns nil only after revocation and
// proof that ALL owned processes have stopped, even after parent loss.
type CustodyChild interface {
	Done() <-chan struct{}
	Stop(context.Context) error
}

// CustodyStarter starts at most one contained launch. Nil child on
// error guarantees no owned process remains. On uncertain startup
// return a nonnil cleanup handle. This context bounds startup only.
type CustodyStarter func(context.Context) (CustodyChild,error)

// Read beyond the Windows lock byte using a separate descriptor. The
// disk record is never itself authority to activate.
func verifyCustodyFence(root *os.Root,generation string,states ...byte) (byte,error) {
	if !validGeneration(generation){return 0,ErrWorkerRecoveryRequired}
	info,err:=root.Lstat(lockFile)
	if err!=nil{return 0,err}
	if !info.Mode().IsRegular() || info.Size()!=int64(3+generationSize){return 0,ErrWorkerRecoveryRequired}
	f,err:=root.Open(lockFile)
	if err!=nil{return 0,err}
	defer f.Close()
	opened,err:=f.Stat()
	if err!=nil{return 0,err}
	if !opened.Mode().IsRegular() || !os.SameFile(info,opened){return 0,ErrWorkerRecoveryRequired}
	var buf [2+generationSize+1]byte
	n,err:=f.ReadAt(buf[:],1)
	if err!=nil&&!errors.Is(err,io.EOF){return 0,err}
	if n!=2+generationSize || buf[0]!=custodyFenceVersion ||
		hex.EncodeToString(buf[2:n])!=generation{return 0,ErrWorkerRecoveryRequired}
	for _,state:=range states{if buf[1]==state{return state,nil}}
	return 0,ErrWorkerRecoveryRequired
}

// RunCustodian runs in a transient trusted process outside its worker
// kill group/job. Retain the opened profile root across cleanup. Only
// the explicit inherited activation byte permits a single launch;
// subsequent bytes or EOF revoke parent liveness. Do not start the
// custodian with a parent-owned CommandContext that kills it on cancel.
// No receipt on unconfirmed cleanup. No WAN, listener or automatic replay.
func RunCustodian(ctx context.Context,profileDir,generation string,parentLife *os.File,start CustodyStarter) error {
	return runCustodian(ctx,profileDir,generation,parentLife,nil,start)
}

// RunCustodianReady sends one byte on a dedicated readiness channel
// after verifying the generation/root and before waiting for activation.
// The trusted launcher must not arm before receiving this marker. No
// application log or worker output belongs on this channel.
func RunCustodianReady(ctx context.Context,profileDir,generation string,parentLife *os.File,ready io.Writer,start CustodyStarter) error {
	if ready==nil{return ErrCustodyActivation}
	return runCustodian(ctx,profileDir,generation,parentLife,ready,start)
}

func runCustodian(ctx context.Context,profileDir,generation string,parentLife *os.File,ready io.Writer,start CustodyStarter) error {
	if parentLife==nil{return ErrCustodyActivation}
	defer parentLife.Close()
	if ctx==nil||start==nil||!filepath.IsAbs(profileDir)||!validGeneration(generation){return ErrCustodyActivation}
	info,err:=parentLife.Stat()
	if err!=nil||info.Mode()&os.ModeNamedPipe==0{return errors.Join(ErrCustodyActivation,err)}
	root,err:=os.OpenRoot(profileDir)
	if err!=nil{return err}
	defer root.Close()
	if _,err:=verifyCustodyFence(root,generation,fencePending,fenceDirty);err!=nil{return err}
	// Publication is the final mutation. Never reopen the profile path.
	finish:=func()error{
		state,err:=verifyCustodyFence(root,generation,fencePending,fenceDirty)
		if err!=nil{return err}
		if state==fencePending{return nil} // no worker could have started
		return recordCleanupReceipt(root,generation)
	}
	lifetime,cancel:=context.WithCancel(ctx)
	defer cancel()
	finished:=make(chan struct{})
	defer close(finished)
	go func(){
		select{
		case <-lifetime.Done():_ = parentLife.Close()
		case <-finished:
		}
	}()
	if ready!=nil{
		n,err:=ready.Write([]byte{custodyReady})
		if n!=1||err!=nil{return errors.Join(ErrCustodyActivation,err,finish())}
	}
	var one [1]byte
	n,err:=io.ReadFull(parentLife,one[:])
	if err!=nil||n!=1||one[0]!=custodyActivate||lifetime.Err()!=nil{
		// No launch attempted; this channel cannot be activated again.
		return errors.Join(ErrCustodyActivation,err,lifetime.Err(),finish())
	}
	// Even a received activation byte is insufficient without the exact
	// durable armed generation. A stale or premature arm launches nothing.
	if _,err:=verifyCustodyFence(root,generation,fenceDirty);err!=nil{return errors.Join(ErrCustodyActivation,err)}
	go func(){var b [1]byte;_,_=parentLife.Read(b[:]);cancel()}()
	startup,stopStartup:=context.WithTimeout(lifetime,custodyStartupTimeout)
	child,startErr:=start(startup)
	stopStartup()
	if child==nil{
		if startErr==nil{return ErrCustodyUnconfirmed}
		return errors.Join(startErr,finish())
	}
	done:=child.Done()
	if startErr==nil&&done!=nil{
		select{
		case <-lifetime.Done():
		case <-done:
		}
	}else if done==nil{
		startErr=errors.Join(startErr,ErrCustodyUnconfirmed)
	}
	cleanup,stopCleanup:=context.WithTimeout(context.WithoutCancel(ctx),custodyStopTimeout)
	defer stopCleanup()
	if err:=child.Stop(cleanup);err!=nil{return errors.Join(startErr,err)}
	return errors.Join(startErr,finish())
}
