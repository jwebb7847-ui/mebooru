package acquisitionpolicy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type fixtureCustodyChild struct {
	cmd *exec.Cmd
	in io.WriteCloser
	done chan struct{}
	err error
	dir string
}
func(c *fixtureCustodyChild)Done()<-chan struct{}{return c.done}
func(c *fixtureCustodyChild)Stop(ctx context.Context)error{
	// Deterministic test-only pause while the leaf is alive.
	_,_=os.Stdout.WriteString("cleanup-pending\n")
	tick:=time.NewTicker(10*time.Millisecond)
	defer tick.Stop()
	for {
		if _,err:=os.Stat(filepath.Join(c.dir,"fixture-allow-cleanup"));err==nil{break}
		select{
		case <-ctx.Done():
			_ = c.in.Close()
			_ = c.cmd.Process.Kill()
			<-c.done
			return ctx.Err()
		case <-tick.C:
		}
	}
	_ = c.in.Close()
	select{
	case <-c.done:return c.err
	case <-ctx.Done():
		_ = c.cmd.Process.Kill()
		<-c.done
		return ctx.Err()
	}
}

func custodyFixtureCommand(mode,dir,generation string)*exec.Cmd{
	exe,err:=os.Executable()
	if err!=nil{panic(err)}
	cmd:=exec.Command(exe,"-test.run=^TestCustodyProcessFixture$")
	cmd.Env=append(os.Environ(),"MEBOORU_TEST_CUSTODY_MODE="+mode,"MEBOORU_TEST_CUSTODY_DIR="+dir,"MEBOORU_TEST_CUSTODY_GENERATION="+generation)
	cmd.Stdout=os.Stdout
	cmd.Stderr=os.Stderr
	return cmd
}

func TestCustodyProcessFixture(t *testing.T){
	mode:=os.Getenv("MEBOORU_TEST_CUSTODY_MODE")
	dir:=os.Getenv("MEBOORU_TEST_CUSTODY_DIR")
	gen:=os.Getenv("MEBOORU_TEST_CUSTODY_GENERATION")
	switch mode{
	case "library", "library-contained", "library-pending":
		l,err:=LockProfile(dir)
		if err!=nil{os.Exit(2)}
		f,err:=l.PrepareCustody()
		if err!=nil{os.Exit(3)}
		if mode=="library-pending"{
			_,_=os.Stdout.WriteString("pending-ready\n")
			var b [1]byte;_,_=os.Stdin.Read(b[:]);runtime.KeepAlive(f);runtime.KeepAlive(l);os.Exit(7)
		}
		childMode:="custodian"
		if mode=="library-contained"{childMode="custodian-contained"}
		cmd:=custodyFixtureCommand(childMode,dir,f.Generation())
		life,err:=cmd.StdinPipe()
		if err!=nil{os.Exit(4)}
		if err:=cmd.Start();err!=nil{os.Exit(4)}
		if err:=f.Arm();err!=nil{os.Exit(5)}
		if _,err:=life.Write([]byte{custodyActivate});err!=nil{os.Exit(5)}
		var b [1]byte
		_,_=os.Stdin.Read(b[:])
		runtime.KeepAlive(life)
		runtime.KeepAlive(f)
		runtime.KeepAlive(l)
		os.Exit(7)
	case "custodian", "custodian-contained":
		ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second)
		defer cancel()
		err:=RunCustodian(ctx,dir,gen,os.Stdin,func(start context.Context)(CustodyChild,error){
			if mode=="custodian-contained"{
				child,err:=startPlatformCustodyFixture(dir)
				if child==nil{return nil,err}
				return &delayedPlatformFixture{CustodyChild:child,dir:dir},err
			}
			cmd:=custodyFixtureCommand("leaf",dir,"")
			in,err:=cmd.StdinPipe()
			if err!=nil{return nil,err}
			if err:=cmd.Start();err!=nil{_ = in.Close();return nil,err}
			c:=&fixtureCustodyChild{cmd:cmd,in:in,done:make(chan struct{}),dir:dir}
			go func(){c.err=cmd.Wait();close(c.done)}()
			return c,nil
		})
		if err!=nil{_,_=os.Stderr.WriteString(err.Error()+"\n");os.Exit(8)}
		_,_=os.Stdout.WriteString("custody-complete\n")
		os.Exit(0)
	case "leaf", "platform-leaf":
		// Trusted test leaf creates no descendants.
		_,_=os.Stdout.WriteString("worker-ready\n")
		done:=make(chan struct{})
		go func(){var b [1]byte;_,_=os.Stdin.Read(b[:]);close(done)}()
		select{
		case <-done:os.Exit(0)
		case <-time.After(25*time.Second):os.Exit(9)
		}
	default:
		runPlatformCustodyFixture(mode,dir)
	}
}

func readCustodyLine(t *testing.T,ctx context.Context,r *bufio.Reader,want string){
	t.Helper()
	ch:=make(chan string,1)
	go func(){line,_:=r.ReadString('\n');ch<-line}()
	select{
	case line:=<-ch:if line!=want+"\n"{t.Fatalf("wanted %q, got %q",want,line)}
	case <-ctx.Done():t.Fatal(ctx.Err())
	}
}

func TestCustodyRecoversAfterAbruptLibraryLoss(t *testing.T){
	testCustodyRecovery(t,"library")
}

func TestContainedCustodyRecoversAfterAbruptLibraryLoss(t *testing.T){
	if runtime.GOOS!="darwin"&&runtime.GOOS!="linux"&&runtime.GOOS!="windows"{t.Skip("platform containment unsupported")}
	testCustodyRecovery(t,"library-contained")
}

func testCustodyRecovery(t *testing.T,mode string){
	t.Helper()
	ctx,cancel:=context.WithTimeout(context.Background(),25*time.Second)
	defer cancel()
	dir:=t.TempDir()
	defer os.WriteFile(filepath.Join(dir,"fixture-allow-cleanup"),nil,0o600)
	cmd:=custodyFixtureCommand(mode,dir,"")
	in,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	defer in.Close()
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close()
	defer w.Close()
	cmd.Stdout=w
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	_ = w.Close()
	reader:=bufio.NewReader(r)
	readCustodyLine(t,ctx,reader,"worker-ready")
	if err:=cmd.Process.Kill();err!=nil{t.Fatal(err)}
	_ = cmd.Wait()
	waited=true
	readCustodyLine(t,ctx,reader,"cleanup-pending")
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	if f,err:=l.BeginWorker();f!=nil||!errors.Is(err,ErrWorkerRecoveryRequired){
		t.Fatalf("replacement before custody proof: %v",err)
	}
	if err:=os.WriteFile(filepath.Join(dir,"fixture-allow-cleanup"),nil,0o600);err!=nil{t.Fatal(err)}
	readCustodyLine(t,ctx,reader,"custody-complete")
	f,err:=l.BeginWorker()
	if err!=nil{t.Fatal(err)}
	if err:=f.ConfirmStopped();err!=nil{t.Fatal(err)}
}

func TestCustodyUnarmedExitPublishesNoChildProof(t *testing.T){
	dir:=t.TempDir()
	seedDirtyGeneration(t,dir,"a")
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	rec,err:=l.readFence()
	if err!=nil{t.Fatal(err)}
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close()
	_ = w.Close()
	err=RunCustodian(context.Background(),dir,rec.generationID(),r,func(context.Context)(CustodyChild,error){
		t.Fatal("unarmed custodian launched")
		return nil,errors.New("unexpected launch")
	})
	if !errors.Is(err,ErrCustodyActivation){t.Fatalf("activation: %v",err)}
	if err:=l.checkRecovery();err!=nil{t.Fatal(err)}
}

type failedCustodyChild struct{done chan struct{}}
func(c failedCustodyChild)Done()<-chan struct{}{return c.done}
func(c failedCustodyChild)Stop(context.Context)error{return ErrCustodyUnconfirmed}

func TestCustodyUnconfirmedStopNeverPublishesReceipt(t *testing.T){
	dir:=t.TempDir()
	seedDirtyGeneration(t,dir,"a")
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	rec,err:=l.readFence()
	if err!=nil{t.Fatal(err)}
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close()
	defer w.Close()
	if _,err:=w.Write([]byte{custodyActivate});err!=nil{t.Fatal(err)}
	done:=make(chan struct{})
	close(done)
	err=RunCustodian(context.Background(),dir,rec.generationID(),r,func(context.Context)(CustodyChild,error){
		return failedCustodyChild{done:done},nil
	})
	if !errors.Is(err,ErrCustodyUnconfirmed){t.Fatalf("stop: %v",err)}
	if err:=l.checkRecovery();!errors.Is(err,ErrWorkerRecoveryRequired){t.Fatalf("uncertain stop recovered: %v",err)}
	if _,err:=os.Stat(filepath.Join(dir,cleanupReceiptFile));!errors.Is(err,os.ErrNotExist){t.Fatalf("unexpected receipt: %v",err)}
}

// Delay only the disposable test's cleanup to make the dirty-window
// assertion deterministic. Production custody does not poll this file.
type delayedPlatformFixture struct{
	CustodyChild
	dir string
}
func(c *delayedPlatformFixture)Stop(ctx context.Context)error{
	_,_=os.Stdout.WriteString("cleanup-pending\n")
	tick:=time.NewTicker(10*time.Millisecond)
	defer tick.Stop()
	for{
		if _,err:=os.Stat(filepath.Join(c.dir,"fixture-allow-cleanup"));err==nil{break}
		select{
		case <-ctx.Done():
			cleanup,cancel:=context.WithTimeout(context.Background(),3*time.Second)
			defer cancel()
			return errors.Join(ctx.Err(),c.CustodyChild.Stop(cleanup))
		case <-tick.C:
		}
	}
	return c.CustodyChild.Stop(ctx)
}

// Actual process crash before any custodian starts must not leave an
// armed marker. No worker could have been activated.
func TestPendingCustodyRecoversBeforeCustodianStartup(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	dir:=t.TempDir()
	cmd:=custodyFixtureCommand("library-pending",dir,"")
	in,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	defer in.Close()
	out,err:=cmd.StdoutPipe()
	if err!=nil{t.Fatal(err)}
	if err:=cmd.Start();err!=nil{t.Fatal(err)}
	waited:=false
	defer func(){if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}}()
	readCustodyLine(t,ctx,bufio.NewReader(out),"pending-ready")
	if err:=cmd.Process.Kill();err!=nil{t.Fatal(err)}
	_ = cmd.Wait()
	waited=true
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	f,err:=l.PrepareCustody()
	if err!=nil{t.Fatal(err)}
	if err:=f.Arm();err!=nil{t.Fatal(err)}
	if err:=f.ConfirmStopped();err!=nil{t.Fatal(err)}
}

func TestCustodianRefusesActivationWithoutDurableArm(t *testing.T){
	dir:=t.TempDir()
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	f,err:=l.PrepareCustody()
	if err!=nil{t.Fatal(err)}
	r,w,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer r.Close()
	defer w.Close()
	if _,err:=w.Write([]byte{custodyActivate});err!=nil{t.Fatal(err)}
	err=RunCustodian(context.Background(),dir,f.Generation(),r,func(context.Context)(CustodyChild,error){
		t.Fatal("pending custodian activated")
		return nil,errors.New("unexpected launch")
	})
	if !errors.Is(err,ErrCustodyActivation){t.Fatalf("activation: %v",err)}
	if _,err:=os.Stat(filepath.Join(dir,cleanupReceiptFile));!errors.Is(err,os.ErrNotExist){t.Fatalf("unexpected receipt: %v",err)}
	if err:=f.ConfirmStopped();err!=nil{t.Fatal(err)}
}
