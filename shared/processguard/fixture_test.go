package processguard

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func fixtureCommand(t *testing.T, name, mode string) *exec.Cmd {
	t.Helper()
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	cmd:=exec.Command(exe,"-test.run=^"+name+"$")
	cmd.Env=append(os.Environ(),"MEBOORU_GUARD_FIXTURE="+mode)
	cmd.Stderr=os.Stderr
	return cmd
}

// Witness stdout is inherited by the harmless grandchild. EOF after
// readiness demonstrates that every fixture writer closed; it does not
// prove arbitrary processes cannot detach or close their own witness.
func witnessReady(t *testing.T, ctx context.Context, r *os.File) *bufio.Reader {
	t.Helper()
	reader:=bufio.NewReader(r)
	type result struct{ line string; err error }
	ch:=make(chan result,1)
	go func(){line,err:=reader.ReadString('\n');ch<-result{line,err}}()
	select{
	case got:=<-ch:
		if got.err!=nil||strings.TrimSpace(got.line)!="ready"{t.Fatalf("fixture readiness: %q %v",got.line,got.err)}
	case <-ctx.Done():t.Fatal(ctx.Err())
	}
	return reader
}

func witnessEOF(t *testing.T,ctx context.Context,r *bufio.Reader){
	t.Helper()
	ch:=make(chan error,1)
	go func(){_,err:=r.ReadByte();ch<-err}()
	select{
	case err:=<-ch:
		if !errors.Is(err,io.EOF){t.Fatalf("expected witness EOF, got %v",err)}
	case <-ctx.Done():t.Fatal("descendant witness remained open: ",ctx.Err())
	}
}

// The grandchild never accesses media, the network, or application state.
// Bound its lifetime independently so a failed test cannot leave it alive
// indefinitely. Stdout deliberately remains open.
func TestGuardGrandchild(t *testing.T){
	if os.Getenv("MEBOORU_GUARD_FIXTURE")!="grandchild"{return}
	_,_ = os.Stdout.WriteString("ready\n")
	if os.Getenv("MEBOORU_GRANDCHILD_READY_FD")=="1"{
		f:=os.NewFile(3,"grandchild-ready")
		_,_ = f.Write([]byte{1})
		_ = f.Close()
	}
	time.Sleep(30*time.Second)
	os.Exit(8)
}
