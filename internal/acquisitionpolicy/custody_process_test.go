package acquisitionpolicy

import(
	"context"
	"errors"
	"os"
	"os/exec"
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
