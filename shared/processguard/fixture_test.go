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


func TestUnrelatedGuardFixture(t *testing.T){
	if os.Getenv("MEBOORU_GUARD_FIXTURE")!="unrelated"{return}
	_,_=os.Stdout.WriteString("ready\n")
	var one [1]byte
	if n,err:=os.Stdin.Read(one[:]);n!=1||err!=nil||one[0]!=1{os.Exit(3)}
	_,_=os.Stdout.WriteString("survived\n")
	os.Exit(0)
}

// The returned closure proves an unrelated process still accepts work
// after the tested containment boundary terminates its own descendants.
func unrelatedWitness(t *testing.T,ctx context.Context)func(){
	t.Helper()
	cmd:=fixtureCommand(t,"TestUnrelatedGuardFixture","unrelated")
	in,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	r,w,err:=os.Pipe()
	if err!=nil{_ = in.Close();t.Fatal(err)}
	cmd.Stdout=w
	if err:=cmd.Start();err!=nil{_ = in.Close();_ = r.Close();_ = w.Close();t.Fatal(err)}
	_ = w.Close()
	waited:=false
	t.Cleanup(func(){
		_ = in.Close()
		_ = r.Close()
		if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}
	})
	reader:=witnessReady(t,ctx,r)
	return func(){
		t.Helper()
		if _,err:=in.Write([]byte{1});err!=nil{t.Fatal("unrelated process stopped: ",err)}
		_ = in.Close()
		ch:=make(chan string,1)
		go func(){line,_:=reader.ReadString('\n');ch<-line}()
		select{
		case line:=<-ch:
			if strings.TrimSpace(line)!="survived"{t.Fatalf("unrelated witness: %q",line)}
		case <-ctx.Done():t.Fatal(ctx.Err())
		}
		witnessEOF(t,ctx,reader)
		err:=cmd.Wait()
		waited=true
		if err!=nil{t.Fatalf("unrelated process failed: %v",err)}
	}
}
