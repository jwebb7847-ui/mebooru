package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func dispatchManagedProbe(args []string)bool{
	if len(args)==0||(args[0]!="internal-managed-worker-probe-v1"&&args[0]!="internal-managed-worker-loss-probe-v1"){return false}
	abortWorker:=args[0]=="internal-managed-worker-loss-probe-v1"
	ctx,cancel:=context.WithTimeout(context.Background(),45*time.Second)
	defer cancel()
	r,w,br,bw,err:=managedProbeChannels(args[1:])
	if err==nil{
		defer r.Close();defer w.Close()
		var broker *workerproto.Session
		broker,err=workerproto.OpenSession(ctx,br,bw,workerproto.OperationRequester,nil)
		if err==nil{
			defer broker.Close()
			go func(){<-broker.Done();cancel()}()
			client,e:=workerproto.NewProbeClient(broker)
			if e!=nil{err=e}else if abortWorker{
				err=workerproto.ServeWorkerLossProbe(ctx,r,w,client,func(){os.Exit(17)})
			}else{err=workerproto.ServeLifecycleProbeWithBroker(ctx,r,w,client)}
		}
	}
	if err!=nil{fmt.Fprintln(os.Stderr,"managed worker probe refused:",err);os.Exit(2)}
	return true
}
