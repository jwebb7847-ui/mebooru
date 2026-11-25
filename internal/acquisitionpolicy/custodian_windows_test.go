//go:build windows

package acquisitionpolicy

import (
	"os"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

func startPlatformCustodyFixture(dir string)(CustodyChild,error){
	cmd:=custodyFixtureCommand("platform-worker",dir,"")
	child,err:=processguard.StartDormantJob(cmd)
	if child==nil{return nil,err}
	if err!=nil{return child,err}
	return child,child.Activate()
}

func runPlatformCustodyFixture(mode,dir string){
	if mode!="platform-worker"{return}
	if err:=processguard.AwaitJobActivation(os.Stdin);err!=nil{os.Exit(2)}
	leaf:=custodyFixtureCommand("platform-leaf",dir,"")
	leaf.Stdin=os.Stdin
	if err:=leaf.Run();err!=nil{os.Exit(4)}
	os.Exit(0)
}
