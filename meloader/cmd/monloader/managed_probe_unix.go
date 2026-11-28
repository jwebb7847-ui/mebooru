//go:build linux || darwin

package main

import (
	"os"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)
func managedProbeChannels(args []string)(*os.File,*os.File,*os.File,*os.File,error){
	if len(args)!=0{return nil,nil,nil,nil,processguard.ErrInheritedPipes}
	life:=os.NewFile(3,"guard-liveness")
	done,err:=processguard.WatchGuard(life)
	if err!=nil{return nil,nil,nil,nil,err}
	go func(){<-done;os.Exit(3)}()
	br,bw,err:=processguard.AdoptGuardBrokerPipes()
	return os.Stdin,os.Stdout,br,bw,err
}
