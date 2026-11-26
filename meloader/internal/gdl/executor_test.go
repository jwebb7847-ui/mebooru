package gdl

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/monbooru/monloader/internal/config"
)

type executorFixture struct{
	capture func(context.Context,string,[]string)processCompletion
	stream func(context.Context,string,[]string,func(io.Reader)error)processCompletion
}
func(f executorFixture)Capture(ctx context.Context,bin string,args []string)processCompletion{
	return f.capture(ctx,bin,args)
}
func(f executorFixture)Stream(ctx context.Context,bin string,args []string,consume func(io.Reader)error)processCompletion{
	return f.stream(ctx,bin,args,consume)
}
func testExecutionTool() *Tool{
	return New(config.NewProvider(config.Default()),SiteLists{})
}

func TestExecutionCaptureRequiresIndependentProof(t *testing.T){
	tool:=testExecutionTool()
	tool.requireCleanupProof=true
	tool.executor=executorFixture{capture:func(context.Context,string,[]string)processCompletion{
		return processCompletion{started:true,runResult:runResult{stdout:[]byte("1.32.19\n")}}
	}}
	if _,err:=tool.runWithBinary(context.Background(),"fixture","--version");!errors.Is(err,errExecutionUnconfirmed){t.Fatal(err)}
	tool.executor=executorFixture{capture:func(context.Context,string,[]string)processCompletion{
		return processCompletion{started:true,cleanupConfirmed:true,runResult:runResult{stdout:[]byte("future-version\n"),exitCode:3}}
	}}
	res,err:=tool.runWithBinary(context.Background(),"fixture","--version")
	if err!=nil||res.exitCode!=3||string(res.stdout)!="future-version\n"{t.Fatalf("result: %+v %v",res,err)}
	tool.executor=nil
	if _,err:=tool.runWithBinary(context.Background(),"fixture","--version");!errors.Is(err,errExecutionUnavailable){t.Fatal(err)}
}

func TestExecutionCatchUpCannotMaskMissingCleanupProof(t *testing.T){
	for _,tc:=range []struct{name string; required,proof bool; cleanupErr error; wantErr bool}{
		{"legacy",false,false,nil,false},
		{"managed-confirmed",true,true,nil,false},
		{"managed-unconfirmed",true,false,nil,true},
		{"managed-failed",true,true,errors.New("descendant live"),true},
	}{
		t.Run(tc.name,func(t *testing.T){
			tool:=testExecutionTool()
			tool.requireCleanupProof=tc.required
			tool.executor=executorFixture{stream:func(ctx context.Context,_ string,_ []string,consume func(io.Reader)error)processCompletion{
				err:=consume(strings.NewReader(strings.Repeat("# skipped\n",catchUpSkips)))
				if ctx.Err()==nil{t.Fatal("catch-up did not cancel its own operation")}
				return processCompletion{started:true,cleanupConfirmed:tc.proof,cleanupErr:tc.cleanupErr,
					runResult:runResult{exitCode:-1},readErr:err}
			}}
			ctx:=context.Background()
			got,err:=tool.download(ctx,"https://example.invalid/test","",t.TempDir(),ArchiveCatchUp,nil,false,"","fixture",nil)
			if (err!=nil)!=tc.wantErr||len(got)!=catchUpSkips||ctx.Err()!=nil{t.Fatalf("catch-up: %d %v",len(got),err)}
			if tc.wantErr&&!strings.Contains(err.Error(),errExecutionUnconfirmed.Error()){t.Fatal(err)}
		})
	}
}

func TestStrictQualificationUsesSelectedExecutor(t *testing.T){
	tool:=testExecutionTool()
	tool.requireCleanupProof=true
	calls:=0
	tool.executor=executorFixture{capture:func(_ context.Context,_ string,args []string)processCompletion{
		calls++
		if len(args)!=1||args[0]!="--version"{t.Fatalf("unqualified version reached probe: %v",args)}
		return processCompletion{started:true,cleanupConfirmed:true,runResult:runResult{stdout:[]byte("1.32.20\n")}}
	}}
	dir:=t.TempDir()
	approved:=[]Item{{ID:"42",Category:"derpibooru",Subcategory:"search",MediaURL:"https://example.invalid/synthetic.jpg",
		Meta:map[string]any{"id":"42","category":"derpibooru","tags":[]string{"safe"}}}}
	_,err:=tool.DownloadStrict(context.Background(),"https://example.invalid/test","",dir,ArchiveRead,nil,approved)
	if err==nil||calls!=1||!strings.Contains(err.Error(),strictQualifiedVersion){t.Fatalf("qualification: %d %v",calls,err)}
	if !strictVersionQualified("1.32.19")||strictVersionQualified("1.32.20")||strictVersionQualified("1.32.19-dev"){t.Fatal("qualification changed")}
}

func TestLegacyExecutorFixture(t *testing.T){
	if os.Getenv("MEBOORU_TEST_EXECUTOR_FIXTURE")!="1"{return}
	_,_=os.Stdout.WriteString("fixture-out\n")
	_,_=os.Stderr.WriteString("fixture-err\n")
	os.Exit(7)
}
func TestLegacyExecutorCaptureAndStream(t *testing.T){
	t.Setenv("MEBOORU_TEST_EXECUTOR_FIXTURE","1")
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	args:=[]string{"-test.run=^TestLegacyExecutorFixture$"}
	executor:=legacyProcessExecutor{}
	c:=executor.Capture(context.Background(),exe,args)
	if !c.started||c.runErr!=nil||c.exitCode!=7||string(c.stdout)!="fixture-out\n"||c.stderr!="fixture-err"||c.cleanupConfirmed{t.Fatalf("capture: %+v",c)}
	var output []byte
	c=executor.Stream(context.Background(),exe,args,func(r io.Reader)error{
		var err error
		output,err=io.ReadAll(r)
		return err
	})
	if !c.started||c.runErr!=nil||c.readErr!=nil||c.exitCode!=7||string(output)!="fixture-out\n"||c.stderr!="fixture-err"||c.cleanupConfirmed{t.Fatalf("stream: %+v %q",c,output)}
}

func TestProofRequiredExecutorRejectsLegacyBeforeLaunch(t *testing.T){
	tool:=testExecutionTool()
	tool.requireCleanupProof=true
	for _,executor:=range []processExecutor{legacyProcessExecutor{},&legacyProcessExecutor{}}{
		tool.executor=executor
		if _,err:=tool.runWithBinary(context.Background(),"never-execute-this-binary","--version");!errors.Is(err,errExecutionUnavailable){t.Fatalf("legacy capture: %v",err)}
		c:=tool.stream(context.Background(),"never-execute-this-binary",nil,func(io.Reader)error{t.Fatal("legacy stream executed");return nil})
		if c.started||!errors.Is(c.runErr,errExecutionUnavailable)||!c.cleanupConfirmed{t.Fatalf("legacy stream: %+v",c)}
	}
}

func TestStrictPreflightRefusesUnconfirmedExecutor(t *testing.T){
	tool:=testExecutionTool()
	tool.requireCleanupProof=true
	calls:=0
	tool.executor=executorFixture{capture:func(_ context.Context,_ string,args []string)processCompletion{
		calls++
		if calls==1{
			return processCompletion{started:true,cleanupConfirmed:true,runResult:runResult{stdout:[]byte(strictQualifiedVersion+"\n")}}
		}
		if calls!=2{t.Fatal("preflight continued after failed proof")}
		// Correct looking negative predicate result cannot authorize a
		// transfer if the runtime boundary has not been confirmed empty.
		return processCompletion{started:true,runResult:runResult{stdout:[]byte(`[[2,{}]]`)}}
	},stream:func(context.Context,string,[]string,func(io.Reader)error)processCompletion{
		t.Fatal("transfer started after unconfirmed preflight")
		return processCompletion{}
	}}
	approved:=[]Item{{ID:"42",Category:"derpibooru",Subcategory:"search",MediaURL:"https://example.invalid/synthetic.jpg",Meta:map[string]any{"id":"42","category":"derpibooru","tags":[]string{"safe"}}}}
	_,err:=tool.DownloadStrict(context.Background(),"https://example.invalid/test","",t.TempDir(),ArchiveRead,nil,approved)
	if err==nil||calls!=2{t.Fatalf("preflight: %d %v",calls,err)}
}
