//go:build darwin || linux

package pipeline

import (
	"os"
	"os/exec"
	"testing"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

func startComposedRuntimeFixture(cmd *exec.Cmd)(processguard.OperationChild,error){
	g,err:=processguard.StartDormantGuard(cmd)
	if g==nil{return nil,err}
	return g,err
}

func TestComposedRuntimeChildFixture(t *testing.T){
	switch os.Getenv("MEBOORU_COMPOSED_FIXTURE"){
	case "runtime":
		worker:=composedRuntimeCommand(t,"leaf")
		worker.Stdout=os.Stdout
		_ = processguard.RunDormantGuard(os.NewFile(3,"parent-life"),worker)
		os.Exit(2)
	case "leaf":
		done,err:=processguard.WatchGuard(os.NewFile(3,"guard-life"))
		if err!=nil{os.Exit(3)}
		go func(){<-done;os.Exit(4)}()
		runComposedRuntimeFixture()
	}
}
