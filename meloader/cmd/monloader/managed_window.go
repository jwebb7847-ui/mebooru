package main

import(
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/managedqa"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/pipeline"
	"github.com/monbooru/monloader/internal/queue"
)

// The real managed worker queue is served over inherited private
// channels. This first executable binding is restricted to the
// controller-created disposable fixture; no legacy HTTP listener,
// pairing, production config, real gallery or external taxonomy calls.
func runManagedWindowWorker(args []string)error{
	if len(args)<1{return managedqa.ErrFixture}
	dir:=args[0]
	r,w,br,bw,err:=managedProbeChannels(args[1:]);if err!=nil{return err}
	defer r.Close();defer w.Close();defer br.Close();defer bw.Close()
	setup,err:=managedqa.Load(dir);if err!=nil{return err}
	ctx,cancel:=context.WithTimeout(context.Background(),12*time.Minute);defer cancel()
	broker,err:=workerproto.OpenSession(ctx,br,bw,workerproto.OperationRequester,nil);if err!=nil{return err}
	defer broker.Close()
	client,err:=processguard.NewManagedRuntimeClient(broker);if err!=nil{return err}
	gate:=workerproto.NewDrainGate(ctx)
	cfg:=config.Default()
	cfg.Tags.StrictBlacklist=true
	cfg.Tags.Blacklist=[]string{"violence"}
	mapper,err:=mapping.New(config.NewProvider(cfg),"");if err!=nil{return err}
	var session atomic.Pointer[workerproto.Session]
	var used atomic.Bool
	md:=setup.Metadata()
	_,mc,identity:=setup.Identity()
	owner:=&pipeline.SessionJobOwner{
		Session:session.Load,
		PrepareWindow:func(c context.Context,j *queue.Job)(*pipeline.ManagedWindowRuntime,error){
			if j.ID!=md.JobID||j.URL!=md.URL||j.Gallery!=md.Gallery||j.MaxItems!=setup.Count||!used.CompareAndSwap(false,true){return nil,managedqa.ErrFixture}
			if err:=client.OpenWindow(c,md);err!=nil{return nil,processguard.ErrOperationUnconfirmed}
			epoch:=gate.CurrentGeneration()
			current:func(c context.Context)bool{return c.Err()==nil&&epoch!=(workerproto.DrainGeneration{})&&gate.CurrentGeneration()==epoch&&broker.Err()==nil}
			return &pipeline.ManagedWindowRuntime{
				Request:md,Identity:identity,Config:mc,PolicyGeneration:1,Mapper:mapper,Client:client,
				Scope:func(c context.Context,q workerproto.MetadataPlanRequest)bool{return q==md&&current(c)},
				Observe:func(c context.Context)(acquisitionplan.MetadataIdentity,uint64,error){if !current(c){return acquisitionplan.MetadataIdentity{},0,managedqa.ErrFixture};return identity,1,nil},
				Transport:windowTaxonomy{},
				Close:func(c context.Context)error{
					// Each proxy's Finish/Stop already acknowledged actual
					// custodian cleanup. Do not close the primary broker on
					// successful job settlement; final profile stop owns it.
					if broker.Err()!=nil{return processguard.ErrOperationUnconfirmed}
					return client.CloseWindow(c,md)
				},
			},nil
		},
		Workspace:func(_ context.Context,g workerproto.StageAllocation)(string,error){
			var known managedqa.Grant
			if err:=managedqa.ReadJSON(dir,managedqa.GrantName,&known,64<<10);err!=nil||known.Allocation!=g{return "",managedqa.ErrFixture}
			return managedqa.Workspace(dir,g)
		},
	}
	proc:=&pipeline.ManagedJobProcessor{Gate:gate,Owner:owner}
	q,err:=queue.NewManagedWithGate(gate,proc,1,20);if err!=nil{return err}
	st,err:=queue.OpenStore(filepath.Join(dir,"queue"));if err!=nil{return err}
	defer st.Close()
	q.UseStore(st);q.Start()
	wake:=make(chan struct{},1)
	go q.RunManagedWatches(ctx,wake)
	notify:=func(){select{case wake<-struct{}{}:default:}}
	defer func(){stop,done:=context.WithTimeout(context.Background(),20*time.Second);defer done();_ = q.CloseManaged(stop)}()
	authorize:=func(c context.Context,op workerproto.Operation,cmd workerproto.QueueCommand)error{
		if c.Err()!=nil{return c.Err()}
		switch op{
		case workerproto.QueueEnqueue:
			if cmd.URL==md.URL&&cmd.Gallery==md.Gallery&&cmd.MaxItems==setup.Count&&!used.Load(){return nil}
		case workerproto.QueueGet,workerproto.QueueList,workerproto.QueuePause,workerproto.QueueResume:
			return nil
		}
		return managedqa.ErrFixture
	}
	s,err:=workerproto.OpenSession(ctx,r,w,workerproto.Worker,q.PrivateDispatch(authorize,notify));if err!=nil{return err}
	defer s.Close();session.Store(s)
	// Session loss cancels all worker admissions, independent of pending
	// queue commands. The outer custodian still owns the real child proof.
	select{case <-s.Done():case <-ctx.Done():case <-broker.Done():}
	stop,done:=context.WithTimeout(context.Background(),20*time.Second);defer done()
	return q.CloseManaged(stop)
}

type windowTaxonomy struct{}
func(windowTaxonomy)RoundTrip(req *http.Request)(*http.Response,error){
	if req.URL.Host!="derpibooru.org"||req.URL.Path!="/api/v1/json/search/tags"||req.Context().Err()!=nil{return nil,managedqa.ErrFixture}
	const body=`{"tags":[{"id":1,"name":"safe","category":"rating","aliased_tag":null,"aliases":[]},{"id":3,"name":"violence","category":null,"aliased_tag":null,"aliases":[]},{"id":4,"name":"fixture","category":null,"aliased_tag":null,"aliases":[]}]}`
	return &http.Response{StatusCode:200,Header:make(http.Header),Body:io.NopCloser(strings.NewReader(body)),Request:req},nil
}

func dispatchManagedWindow(args []string)bool{
	if len(args)==0||args[0]!="internal-managed-window-worker-v1"{return false}
	if err:=runManagedWindowWorker(args[1:]);err!=nil{fmt.Fprintln(os.Stderr,"managed window worker:",err);os.Exit(2)}
	return true
}
