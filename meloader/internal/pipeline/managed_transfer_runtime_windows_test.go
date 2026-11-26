//go:build windows

package pipeline

import (
	"os"
	"os/exec"
	"testing"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

func startComposedRuntimeFixture(cmd *exec.Cmd)(processguard.OperationChild,error){
	g,err:=processguard.StartDormantJob(cmd)
	if g==nil{return nil,err}
	return g,err
}

func TestComposedRuntimeChildFixture(t *testing.T){
	if os.Getenv("MEBOORU_COMPOSED_FIXTURE")!="runtime"{return}
	if err:=processguard.AwaitJobActivation(os.Stdin);err!=nil{os.Exit(2)}
	runComposedRuntimeFixture()
}
