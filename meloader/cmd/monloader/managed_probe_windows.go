//go:build windows

package main

import (
	"os"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)
func managedProbeChannels(args []string)(*os.File,*os.File,*os.File,*os.File,error){
	if len(args)!=1{return nil,nil,nil,nil,processguard.ErrInheritedPipes}
	if err:=processguard.AwaitJobActivation(os.Stdin);err!=nil{return nil,nil,nil,nil,err}
	files,err:=processguard.OpenJobBrokerPipes(args[0])
	if err!=nil{return nil,nil,nil,nil,err}
	return files[0],files[1],files[2],files[3],nil
}
