//go:build darwin || linux

package acquisitionpolicy

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

type unixCustodyFixture struct{
	guard *processguard.DormantGuard
	input io.WriteCloser
}
func(c *unixCustodyFixture)Done()<-chan struct{}{return c.guard.Done()}
func(c *unixCustodyFixture)Stop(ctx context.Context)error{
	// Cooperative input EOF lets the fixture worker reap its leaf
	// before the guard exits. Do not depend on host PID 1 to reap
	// orphan zombies. The final proof is still actual group absence.
	_ = c.input.Close()
	grace:=time.NewTimer(3*time.Second)
	defer grace.Stop()
	select{case <-c.guard.Done():case <-grace.C:case <-ctx.Done():}
	return c.guard.Stop(ctx)
}

func startPlatformCustodyFixture(dir string)(CustodyChild,error){
	cmd:=custodyFixtureCommand("platform-guard",dir,"")
	in,err:=cmd.StdinPipe()
	if err!=nil{return nil,err}
	guard,err:=processguard.StartDormantGuard(cmd)
	if guard==nil{_ = in.Close();return nil,err}
	child:=&unixCustodyFixture{guard:guard,input:in}
	if err!=nil{return child,err}
	return child,guard.Activate()
}

func runPlatformCustodyFixture(mode,dir string){
	switch mode{
	case "platform-guard":
		life:=os.NewFile(3,"custody-liveness")
		worker:=custodyFixtureCommand("platform-worker",dir,"")
		worker.Stdin=os.Stdin
		_ = processguard.RunDormantGuard(life,worker)
		os.Exit(2)
	case "platform-worker":
		life:=os.NewFile(3,"guard-liveness")
		if _,err:=processguard.WatchGuard(life);err!=nil{os.Exit(3)}
		leaf:=custodyFixtureCommand("platform-leaf",dir,"")
		leaf.Stdin=os.Stdin
		if err:=leaf.Run();err!=nil{os.Exit(4)}
		os.Exit(0)
	}
}
