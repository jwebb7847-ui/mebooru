//go:build windows

package processguard

import("os";"testing")

func startContainedOperationFixture(t *testing.T,w *os.File)(OperationChild,error){
	cmd:=fixtureCommand(t,"TestWindowsDormantJobFixture","dormant-job-worker")
	cmd.Stdout=w
	g,err:=StartDormantJob(cmd)
	if g==nil{return nil,err}
	return g,err
}
