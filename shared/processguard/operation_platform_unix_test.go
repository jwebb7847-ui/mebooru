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
	// Fixture worker exits cooperatively on stdin EOF so it can be reaped
	// before group proof; force fallback remains the guard's responsibility.
	_ = c.input.Close()
	t:=time.NewTimer(3*time.Second)
	defer t.Stop()
	select{case <-c.Done():case <-t.C:case <-ctx.Done():}
	return c.DormantGuard.Stop(ctx)
}
func startContainedOperationFixture(t *testing.T,w *os.File)(OperationChild,error){
	cmd:=fixtureCommand(t,"TestDormantSupervisorFixture","supervisor-guard")
	in,err:=cmd.StdinPipe()
	if err!=nil{return nil,err}
	cmd.Stdout=w
	g,err:=StartDormantGuard(cmd)
	if g==nil{_ = in.Close();return nil,err}
	return &containedOperationFixture{DormantGuard:g,input:in},err
}
