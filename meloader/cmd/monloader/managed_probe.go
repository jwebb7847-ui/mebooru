package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func dispatchManagedProbe(args []string)bool{
	if len(args)==0||args[0]!="internal-managed-worker-probe-v1"{return false}
	ctx,cancel:=context.WithTimeout(context.Background(),45*time.Second)
	defer cancel()
	r,w,err:=managedProbeChannels(args[1:])
	if err==nil{err=workerproto.ServeLifecycleProbe(ctx,r,w)}
	if err!=nil{fmt.Fprintln(os.Stderr,"managed worker probe refused:",err);os.Exit(2)}
	return true
}
