package gdl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/desktop"
)

var errExecutionUnconfirmed=errors.New("gallery-dl operation cleanup unconfirmed")
var errExecutionUnavailable=errors.New("gallery-dl executor unavailable")

type runResult struct {
	stdout []byte
	stderr string
	exitCode int
}

// processCompletion distinguishes launch/exit, parsing, and containment.
// An exit code, cancellation, or output EOF is not proof of cleanup.
// A managed executor MUST return cleanupConfirmed only after the
// custodian independently proves its entire operation boundary empty.
// Legacy compatibility mode deliberately makes no such claim.
type processCompletion struct {
	runResult
	started bool
	runErr error
	readErr error
	cleanupErr error
	cleanupConfirmed bool
}
func(c processCompletion)proofError(required bool)error{
	if c.cleanupErr!=nil{return errors.Join(errExecutionUnconfirmed,c.cleanupErr)}
	if required&&!c.cleanupConfirmed{return errExecutionUnconfirmed}
	return nil
}

// This seam is internal to Meloader. bin and args are constructed by
// trusted local callers, NOT a proposed IPC argv or arbitrary exec API.
// Future managed implementation must translate into a narrow typed
// broker contract and must never launch directly or fall back to legacy.
type processExecutor interface{
	Capture(context.Context,string,[]string)processCompletion
	Stream(context.Context,string,[]string,func(io.Reader)error)processCompletion
}

type legacyProcessExecutor struct{}

// Keep the legacy process behavior for existing standalone/HTTP mode.
// Do not confuse WaitDelay or the existing group/PID tree kill with the
// managed supervisor's receipt-backed containment proof.
const waitDelay=5*time.Second

func legacyCommand(ctx context.Context,bin string,args []string)*exec.Cmd{
	cmd:=exec.CommandContext(ctx,bin,args...)
	cmd.WaitDelay=waitDelay
	killGroupOnCancel(cmd)
	desktop.HideConsole(cmd)
	return cmd
}
func(legacyProcessExecutor)Capture(ctx context.Context,bin string,args []string)processCompletion{
	cmd:=legacyCommand(ctx,bin,args)
	var out,errBuf bytes.Buffer
	cmd.Stdout=&out
	cmd.Stderr=&errBuf
	err:=cmd.Run()
	exit,runErr:=exitStatus(err)
	if runErr!=nil{runErr=fmt.Errorf("running %s: %w",bin,runErr)}
	return processCompletion{
		runResult:runResult{stdout:out.Bytes(),stderr:strings.TrimSpace(errBuf.String()),exitCode:exit},
		started:cmd.Process!=nil,runErr:runErr,
	}
}
func(legacyProcessExecutor)Stream(ctx context.Context,bin string,args []string,consume func(io.Reader)error)processCompletion{
	cmd:=legacyCommand(ctx,bin,args)
	stdout,err:=cmd.StdoutPipe()
	if err!=nil{return processCompletion{runErr:err}}
	var errBuf bytes.Buffer
	cmd.Stderr=&errBuf
	if err:=cmd.Start();err!=nil{
		return processCompletion{runErr:fmt.Errorf("running %s: %w",bin,err)}
	}
	// The consumer runs synchronously and drains to EOF before Wait,
	// including when it requests cancellation during archive catch-up.
	readErr:=consume(stdout)
	exit,runErr:=exitStatus(cmd.Wait())
	return processCompletion{
		runResult:runResult{stderr:strings.TrimSpace(errBuf.String()),exitCode:exit},
		started:true,runErr:runErr,readErr:readErr,
	}
}
func exitStatus(err error)(int,error){
	if err==nil{return 0,nil}
	var ee *exec.ExitError
	if errors.As(err,&ee){return ee.ExitCode(),nil}
	return 0,err
}

// Reject the legacy executor before launch in proof-required mode,
// even if accidentally installed by a future constructor.
func(t *Tool)checkExecutor()error{
	if t.executor==nil{return errExecutionUnavailable}
	if t.requireCleanupProof{
		switch t.executor.(type){
		case legacyProcessExecutor,*legacyProcessExecutor:return errExecutionUnavailable
		}
	}
	return nil
}
func(t *Tool)capture(ctx context.Context,bin string,args []string)(runResult,error){
	if err:=t.checkExecutor();err!=nil{return runResult{},err}
	c:=t.executor.Capture(ctx,bin,args)
	if err:=c.proofError(t.requireCleanupProof);err!=nil{return c.runResult,err}
	if !c.started&&c.runErr==nil{return c.runResult,errExecutionUnavailable}
	return c.runResult,c.runErr
}
func(t *Tool)stream(ctx context.Context,bin string,args []string,consume func(io.Reader)error)processCompletion{
	if err:=t.checkExecutor();err!=nil{return processCompletion{runErr:err,cleanupConfirmed:true}}
	c:=t.executor.Stream(ctx,bin,args,consume)
	if !c.started&&c.runErr==nil{c.runErr=errExecutionUnavailable}
	return c
}

// Legacy callers (strict fixture and standalone helpers) are explicit.
// Managed Tool methods must go through their selected executor instead.
func runBinary(ctx context.Context,bin string,args ...string)(runResult,error){
	c:=legacyProcessExecutor{}.Capture(ctx,bin,args)
	return c.runResult,c.runErr
}
