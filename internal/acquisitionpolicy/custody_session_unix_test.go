//go:build darwin || linux

package acquisitionpolicy

import (
	"context"
	"os"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

type sessionFixtureGuard struct{guard *processguard.DormantGuard}
func(c *sessionFixtureGuard)Done()<-chan struct{}{return c.guard.Done()}
func(c *sessionFixtureGuard)Stop(ctx context.Context)error{
	// The library closes its protocol ends on exit. Give the fixture
	// worker a chance to exit and be reaped before forcing its group.
	grace:=time.NewTimer(3*time.Second)
	defer grace.Stop()
	select{case <-c.guard.Done():case <-grace.C:case <-ctx.Done():}
	return c.guard.Stop(ctx)
}
func startSessionFixtureWorker(dir string,r,w *os.File)(CustodyChild,error){
	cmd:=sessionFixtureCommand("guard",dir,"")
	cmd.Stdin=r
	cmd.Stdout=w
	g,err:=processguard.StartDormantGuard(cmd)
	if g==nil{return nil,err}
	child:=&sessionFixtureGuard{guard:g}
	if err!=nil{return child,err}
	return child,g.Activate()
}

func runSessionFixtureWorker(mode,dir string){
	switch mode{
	case "guard":
		life:=os.NewFile(3,"custody-liveness")
		cmd:=sessionFixtureCommand("worker",dir,"")
		cmd.Stdin=os.Stdin
		cmd.Stdout=os.Stdout
		_ = processguard.RunDormantGuard(life,cmd)
		os.Exit(5)
	case "worker":
		life:=os.NewFile(3,"guard-liveness")
		done,err:=processguard.WatchGuard(life)
		if err!=nil{os.Exit(6)}
		go func(){<-done;os.Exit(7)}()
		serveSessionFixture(os.Stdin,os.Stdout)
	}
}
