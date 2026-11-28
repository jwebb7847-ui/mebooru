//go:build darwin || linux

package processguard

import(
	"context"
	"io"
	"os"
	"testing"
	"time"
)

type containedOperationFixture struct{
	*DormantGuard
	input io.WriteCloser
}
func(c *containedOperationFixture)Stop(ctx context.Context)error{
	// Fixture grandchild exits cooperatively on stdin EOF; its parent
	// reaps it before group proof; force fallback remains the guard's responsibility.
	_ = c.input.Close()
	t:=time.NewTimer(3*time.Second)
	defer t.Stop()
	select{case <-c.Done():case <-t.C:case <-ctx.Done():}
	return c.DormantGuard.Stop(ctx)
}
func startContainedOperationFixture(t *testing.T,w *os.File)(OperationChild,error){
	cmd:=fixtureCommand(t,"TestOperationDescendantFixture","operation-guard")
	in,err:=cmd.StdinPipe()
	if err!=nil{return nil,err}
	cmd.Stdout=w
	g,err:=StartDormantGuard(cmd)
	if g==nil{_ = in.Close();return nil,err}
	return &containedOperationFixture{DormantGuard:g,input:in},err
}

// The witness is emitted by a grandchild, not the guard or direct
// operation process. No actual downloader, filesystem or WAN work.
func TestOperationDescendantFixture(t *testing.T){
	switch os.Getenv("MEBOORU_GUARD_FIXTURE"){
	case "operation-guard":
		worker:=fixtureCommand(t,"TestOperationDescendantFixture","operation-worker")
		worker.Stdin,worker.Stdout=os.Stdin,os.Stdout
		_ = RunDormantGuard(os.NewFile(3,"parent-life"),worker)
		os.Exit(2)
	case "operation-worker":
		done,err:=WatchGuard(os.NewFile(3,"guard-life"))
		if err!=nil{os.Exit(3)}
		go func(){<-done;os.Exit(4)}()
		leaf:=fixtureCommand(t,"TestOperationDescendantFixture","operation-leaf")
		leaf.Stdin,leaf.Stdout=os.Stdin,os.Stdout
		if err:=leaf.Run();err!=nil{os.Exit(5)}
		os.Exit(0)
	case "operation-leaf":
		_,_ = os.Stdout.WriteString("ready\n")
		done:=make(chan struct{})
		go func(){var b [1]byte;_,_=os.Stdin.Read(b[:]);close(done)}()
		select{case <-done:os.Exit(0);case <-time.After(30*time.Second):os.Exit(6)}
	}
}
