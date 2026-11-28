//go:build windows

package acquisitionpolicy

import (
	"os"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

func startSessionFixtureWorker(dir string,r,w *os.File)(CustodyChild,error){
	cmd:=sessionFixtureCommand("worker",dir,"")
	d,err:=processguard.StartDormantJobWithPipes(cmd,r,w)
	if d==nil{return nil,err}
	if err!=nil{return d,err}
	return d,d.Activate()
}

func runSessionFixtureWorker(mode,dir string){
	if mode!="worker"{os.Exit(5)}
	if err:=processguard.AwaitJobActivation(os.Stdin);err!=nil{os.Exit(6)}
	r,w,err:=processguard.OpenInheritedPipes(os.Args[len(os.Args)-1])
	if err!=nil{os.Exit(7)}
	serveSessionFixture(r,w)
}
