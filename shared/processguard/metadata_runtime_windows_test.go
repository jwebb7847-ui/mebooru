//go:build windows

package processguard

import (
	"os"
	"os/exec"
	"testing"
)

func startMetadataRuntimeFixture(cmd *exec.Cmd)(OperationChild,error){
	g,err:=StartDormantJob(cmd)
	if g==nil{return nil,err}
	return g,err
}

func TestMetadataRuntimeChildFixture(t *testing.T){
	if os.Getenv("MEBOORU_GUARD_FIXTURE")!="metadata-runtime"{return}
	if err:=AwaitJobActivation(os.Stdin);err!=nil{os.Exit(2)}
	runMetadataRuntimeFixture()
}
