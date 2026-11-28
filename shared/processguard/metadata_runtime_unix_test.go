//go:build darwin || linux

package processguard

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func startMetadataRuntimeFixture(cmd *exec.Cmd)(OperationChild,error){
	g,err:=StartDormantGuard(cmd)
	if g==nil{return nil,err}
	return g,err
}

func TestMetadataRuntimeChildFixture(t *testing.T){
	switch os.Getenv("MEBOORU_GUARD_FIXTURE"){
	case "metadata-runtime":
		worker:=fixtureCommand(t,"TestMetadataRuntimeChildFixture","metadata-runtime-leaf")
		// fixtureCommand inherits parent env. Replace the mode explicitly,
		// not the source arguments or executable with requester data.
		env:=worker.Env[:0]
		for _,v:=range worker.Env{if !strings.HasPrefix(v,"MEBOORU_GUARD_FIXTURE="){env=append(env,v)}}
		worker.Env=append(env,"MEBOORU_GUARD_FIXTURE=metadata-runtime-leaf")
		worker.Stdout=os.Stdout
		_ = RunDormantGuard(os.NewFile(3,"parent-life"),worker)
		os.Exit(2)
	case "metadata-runtime-leaf":
		done,err:=WatchGuard(os.NewFile(3,"guard-life"))
		if err!=nil{os.Exit(3)}
		go func(){<-done;os.Exit(4)}()
		runMetadataRuntimeFixture()
	}
}
