//go:build windows

package main

import (
	"os"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)
func managedProbeChannels(args []string)(*os.File,*os.File,error){
	if len(args)!=1{return nil,nil,processguard.ErrInheritedPipes}
	if err:=processguard.AwaitJobActivation(os.Stdin);err!=nil{return nil,nil,err}
	return processguard.OpenInheritedPipes(args[0])
}
