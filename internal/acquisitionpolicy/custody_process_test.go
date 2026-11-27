package acquisitionpolicy

import(
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type custodyLaunchLeaf struct{done chan struct{};fail bool}
func(c custodyLaunchLeaf)Done()<-chan struct{}{return c.done}
func(c custodyLaunchLeaf)Stop(context.Context)error{
	if c.fail{return ErrCustodyUnconfirmed}
	return nil
}

func TestCustodyLaunchFixture(t *testing.T){
	mode:=os.Getenv("MEBOORU_TEST_CUSTODY_LAUNCH")
	if mode==""{return}
	if mode=="wrong-ready"{_,_=os.Stdout.Write([]byte{99});os.Exit(2)}
	if mode=="hold-ready"{
		// Hold stdout open without writing readiness, independently of
		// parent liveness. Test-only release and a hard lifetime bound.
		dir:=os.Getenv("MEBOORU_TEST_CUSTODY_DIR")
		if err:=os.WriteFile(filepath.Join(dir,"fixture-ready-held"),nil,0600);err!=nil{os.Exit(4)}
		deadline:=time.NewTimer(12*time.Second)
		defer deadline.Stop()
		tick:=time.NewTicker(20*time.Millisecond)
		defer tick.Stop()
		for{
			if _,err:=os.Stat(filepath.Join(dir,"fixture-ready-release"));err==nil{os.Exit(0)}
			select{case <-deadline.C:os.Exit(5);case <-tick.C:}
		}
	}
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
	defer cancel()
	err:=RunCustodianReady(ctx,os.Getenv("MEBOORU_TEST_CUSTODY_DIR"),os.Getenv("MEBOORU_TEST_CUSTODY_GENERATION"),os.Stdin,os.Stdout,func(context.Context)(CustodyChild,error){
		// State-machine fixture only. Actual descendants are separately
		// covered by TestContainedCustodyRecoversAfterAbruptLibraryLoss.
		return custodyLaunchLeaf{done:make(chan struct{}),fail:mode=="unconfirmed"},nil
	})
	if err!=nil{os.Exit(3)}
	os.Exit(0)
}

func TestLibraryCustodyProcessReadinessAndProof(t *testing.T){
	for _,mode:=range []string{"unarmed","armed","unconfirmed","wrong-ready"}{
		t.Run(mode,func(t *testing.T){
			ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
			defer cancel()
			dir:=t.TempDir()
			l,err:=LockProfile(dir)
			if err!=nil{t.Fatal(err)}
			defer l.Close()
			f,err:=l.PrepareCustody()
			if err!=nil{t.Fatal(err)}
			exe,err:=os.Executable()
			if err!=nil{t.Fatal(err)}
			cmd:=exec.Command(exe,"-test.run=^TestCustodyLaunchFixture$")
			cmd.Env=append(os.Environ(),"MEBOORU_TEST_CUSTODY_LAUNCH="+mode,"MEBOORU_TEST_CUSTODY_DIR="+dir,"MEBOORU_TEST_CUSTODY_GENERATION="+f.Generation())
			p,err:=StartCustodyProcess(ctx,cmd,f)
			if p!=nil{defer p.Stop(ctx)}
			if mode=="wrong-ready"{
				if p==nil||!errors.Is(err,ErrCustodyActivation){t.Fatalf("wrong ready: %v",err)}
			}else if err!=nil{t.Fatal(err)}
			if p==nil{t.Fatal("missing cleanup owner")}
			duplicate:=exec.Command(exe,"-test.run=^TestCustodyLaunchFixture$")
			duplicate.Env=cmd.Env
			if other,err:=StartCustodyProcess(ctx,duplicate,f);other!=nil||!errors.Is(err,ErrCustodyActivation){
				if other!=nil{_ = other.Stop(ctx)}
				t.Fatalf("second custodian for generation: %v",err)
			}
			if err:=f.Arm();!errors.Is(err,ErrStaleWorkerFence){t.Fatalf("bypassed custody owner: %v",err)}
			if mode=="armed"||mode=="unconfirmed"{
				if err:=p.Activate();err!=nil{t.Fatal(err)}
				if err:=p.Activate();!errors.Is(err,ErrCustodyActivation){t.Fatalf("second arm: %v",err)}
				if !f.isArmed(){t.Fatal("activation not persisted")}
			}
			err=p.Stop(ctx)
			if mode=="unconfirmed"{
				if !errors.Is(err,ErrWorkerRecoveryRequired){t.Fatalf("missing receipt accepted: %v",err)}
				if err:=l.Close();!errors.Is(err,ErrWorkerRecoveryRequired){t.Fatalf("dirty lock released: %v",err)}
				// Test-only leaf did not spawn anything. Test cleanup can
				// independently prove absence, unlike a production caller.
				if err:=f.ConfirmStopped();err!=nil{t.Fatal(err)}
				return
			}
			if err!=nil{t.Fatal(err)}
			if err:=p.Activate();!errors.Is(err,ErrCustodyActivation){t.Fatalf("arm after stop: %v",err)}
			if err:=p.Stop(ctx);err!=nil{t.Fatal(err)}
			if err:=f.ConfirmStopped();err!=nil{t.Fatal(err)}
		})
	}
}


func TestLibraryCustodyReadinessCancellationWhileWriterAlive(t *testing.T){
	dir:=t.TempDir()
	l,err:=LockProfile(dir)
	if err!=nil{t.Fatal(err)}
	defer l.Close()
	f,err:=l.PrepareCustody()
	if err!=nil{t.Fatal(err)}
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	cmd:=exec.Command(exe,"-test.run=^TestCustodyLaunchFixture$")
	cmd.Env=append(os.Environ(),"MEBOORU_TEST_CUSTODY_LAUNCH=hold-ready","MEBOORU_TEST_CUSTODY_DIR="+dir,"MEBOORU_TEST_CUSTODY_GENERATION="+f.Generation())
	ctx,cancel:=context.WithCancel(context.Background())
	defer cancel()
	release:=filepath.Join(dir,"fixture-ready-release")
	defer os.WriteFile(release,nil,0600)
	type answer struct{p *CustodyProcess;err error}
	returned:=make(chan answer,1)
	go func(){p,err:=StartCustodyProcess(ctx,cmd,f);returned<-answer{p,err}}()
	tick:=time.NewTicker(20*time.Millisecond)
	defer tick.Stop()
	deadline:=time.NewTimer(8*time.Second)
	defer deadline.Stop()
	for{
		if _,err:=os.Stat(filepath.Join(dir,"fixture-ready-held"));err==nil{break}
		select{
		case got:=<-returned:
			if got.p!=nil{_ = os.WriteFile(release,nil,0600);cleanup,stop:=context.WithTimeout(context.Background(),5*time.Second);_ = got.p.Stop(cleanup);stop()}
			t.Fatalf("fixture returned before readiness wait: %v",got.err)
		case <-deadline.C:t.Fatal("fixture did not hold readiness")
		case <-tick.C:
		}
	}
	cancel()
	var got answer
	select{
	case got=<-returned:
	case <-time.After(3*time.Second):
		// Release before failing so the old synchronous-close path
		// can finish and the child is not left behind.
		_ = os.WriteFile(release,nil,0600)
		select{
		case v:=<-returned:
			if v.p!=nil{cleanup,stop:=context.WithTimeout(context.Background(),5*time.Second);_ = v.p.Stop(cleanup);stop()}
		case <-time.After(5*time.Second):
		}
		t.Fatal("readiness cancellation waited for the live writer")
	}
	if got.p==nil||!errors.Is(got.err,context.Canceled){t.Fatalf("canceled readiness: %v",got.err)}
	select{case <-got.p.Done():t.Fatal("fixture writer exited before cancellation proof");default:}
	if err:=got.p.Activate();!errors.Is(err,ErrCustodyActivation){t.Fatal("canceled readiness activated",err)}
	if err:=os.WriteFile(release,nil,0600);err!=nil{t.Fatal(err)}
	cleanup,stop:=context.WithTimeout(context.Background(),5*time.Second)
	defer stop()
	if err:=got.p.Stop(cleanup);err!=nil{t.Fatal(err)}
	if f.isArmed(){t.Fatal("canceled readiness armed fence")}
	if err:=f.ConfirmStopped();err!=nil{t.Fatal(err)}
}
