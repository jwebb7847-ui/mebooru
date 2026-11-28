package main

import(
	"context"
	"encoding/json"
	"errors"
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
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
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
	live,liveErr:=windowregistry.LoadLive(dir)
	var setup managedqa.Setup
	if errors.Is(liveErr,os.ErrNotExist){
		setup,err=managedqa.Load(dir);if err!=nil{return err}
	}else if liveErr!=nil{return liveErr}
	var ctx context.Context
	var cancel context.CancelFunc
	if setup.Application||liveErr==nil {
		ctx, cancel = context.WithCancel(context.Background())
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), 12*time.Minute)
	}
	defer cancel()
	broker,err:=workerproto.OpenSession(ctx,br,bw,workerproto.OperationRequester,nil);if err!=nil{return err}
	defer broker.Close()
	client,err:=processguard.NewManagedRuntimeClient(broker);if err!=nil{return err}
	gate:=workerproto.NewDrainGate(ctx)
	var mapper *mapping.Mapper
	if liveErr!=nil {
		cfg:=config.Default()
		cfg.Tags.StrictBlacklist=true
		cfg.Tags.Blacklist=[]string{"violence"}
		mapper,err=mapping.New(config.NewProvider(cfg),"");if err!=nil{return err}
	}
	var session atomic.Pointer[workerproto.Session]
	_,mc,identity:=setup.Identity()
	var owner *pipeline.SessionJobOwner
	owner=&pipeline.SessionJobOwner{
		Session:session.Load,
		PrepareWindow:func(c context.Context,j *queue.Job)(*pipeline.ManagedWindowRuntime,error){
			md:=setup.MetadataFor(j.ID)
			options,id:=mc,identity
			if liveErr==nil {
				if j.MaxItems<1||j.MaxItems>live.MaxItems||j.Offset<0||j.Offset>workerproto.MaxMetadataPosition-j.MaxItems{return nil,windowregistry.ErrWindow}
				md=workerproto.MetadataPlanRequest{JobID:j.ID,Gallery:j.Gallery,Kind:workerproto.MetadataStrictResolve,Source:workerproto.StrictQueueSource(j.URL),URL:j.URL,First:uint32(j.Offset+1),Last:uint32(j.Offset+j.MaxItems)}
				options,id=live.Identity(md.Source)
			}
			if !workerproto.ValidMetadataPlan(md)||j.URL!=md.URL||j.Gallery!=md.Gallery||(liveErr!=nil&&j.MaxItems!=setup.Count){return nil,managedqa.ErrFixture}
			if err:=c.Err();err!=nil{return nil,err}
			// Registration and claim are local handshakes. Finish them if
			// cancellation arrives mid-request so the processor can close
			// the exact claimed window. Runtime work still uses c.
			handshake,stop:=context.WithTimeout(context.WithoutCancel(c),5*time.Second)
			defer stop()
			if err:=owner.RegisterWindow(handshake,md,j.ManagedIntentID);err!=nil{return nil,processguard.ErrOperationUnconfirmed}
			if err:=client.OpenWindow(handshake,md);err!=nil{return nil,processguard.ErrOperationUnconfirmed}
			epoch:=gate.CurrentGeneration()
			current:=func(c context.Context)bool{return c.Err()==nil&&epoch!=(workerproto.DrainGeneration{})&&gate.CurrentGeneration()==epoch&&broker.Err()==nil}
			runtime:=&pipeline.ManagedWindowRuntime{
				Request:md,Identity:id,Config:options,PolicyGeneration:1,Mapper:mapper,Client:client,
				Scope:func(c context.Context,q workerproto.MetadataPlanRequest)bool{return q==md&&current(c)},
				Observe:func(c context.Context)(acquisitionplan.MetadataIdentity,uint64,error){if !current(c){return acquisitionplan.MetadataIdentity{},0,managedqa.ErrFixture};return id,1,nil},
				Transport:windowTaxonomy{},
				Close:func(c context.Context)error{
					// Each proxy's Finish/Stop already acknowledged actual
					// custodian cleanup. Do not close the primary broker on
					// successful job settlement; final profile stop owns it.
					if broker.Err()!=nil{return processguard.ErrOperationUnconfirmed}
					return client.CloseWindow(c,md)
				},
			}
			if liveErr==nil{
				runtime.Abandon=owner.Abandon
				runtime.Mapper,runtime.Transport=nil,nil
				runtime.ContainedPolicy=func(c context.Context)(windowregistry.LivePolicy,error){
					var p windowregistry.LivePolicy
					if !current(c){return p,workerproto.ErrOffline}
					err:=windowregistry.ReadArtifact(dir,windowregistry.ArtifactName(md.JobID,"policy"),&p,windowregistry.MaxLivePolicy)
					return p,err
				}
			}
			return runtime,nil
		},
		Workspace:func(_ context.Context,g workerproto.StageAllocation)(string,error){
			var known managedqa.Grant
			if liveErr==nil{
				if err:=windowregistry.ReadArtifact(dir,windowregistry.ArtifactName(g.Request.Transfer.Metadata.JobID,"stage"),&known,64<<10);err!=nil||known.Allocation!=g{return "",windowregistry.ErrWindow}
				return windowregistry.Workspace(dir,g)
			}
			if err:=managedqa.ReadJSON(dir,managedqa.JobFile(managedqa.GrantName,g.Request.Transfer.Metadata.JobID),&known,64<<10);err!=nil||known.Allocation!=g{return "",managedqa.ErrFixture}
			return managedqa.Workspace(dir,g)
		},
	}
	proc:=&pipeline.ManagedJobProcessor{Gate:gate,Owner:owner}
	q,err:=queue.NewManagedWithGate(gate,proc,1,20);if err!=nil{return err}
	st,err:=queue.OpenStore(filepath.Join(dir,"queue"));if err!=nil{return err}
	defer st.Close()
	q.UseStore(st)
	if setup.Application||liveErr==nil { q.Pause() }
	q.Start()
	wake:=make(chan struct{},1)
	go q.RunManagedWatches(ctx,wake)
	notify:=func(){select{case wake<-struct{}{}:default:}}
	defer func(){stop,done:=context.WithTimeout(context.Background(),20*time.Second);defer done();_ = q.CloseManaged(stop)}()
	authorize:=func(c context.Context,op workerproto.Operation,cmd workerproto.QueueCommand)error{
		if c.Err()!=nil{return c.Err()}
		switch op{
		case workerproto.QueueEnqueue:
			if !workerproto.ValidQueueIntentID(cmd.IntentID){return managedqa.ErrFixture}
			if liveErr==nil&&workerproto.StrictQueueSource(cmd.URL)!=""&&cmd.MaxItems>=1&&cmd.MaxItems<=live.MaxItems{return nil}
			for job:=int64(1);job<=int64(setup.Windows);job++{md:=setup.MetadataFor(job);if cmd.URL==md.URL&&cmd.Gallery==md.Gallery&&cmd.MaxItems==setup.Count{return nil}}
		case workerproto.QueueGet,workerproto.QueueList,workerproto.QueuePause,workerproto.QueueResume,workerproto.QueueCancel:
			return nil
		case workerproto.WatchList,workerproto.WatchRemove,workerproto.WatchSetInterval:
			if liveErr==nil{return nil}
		case workerproto.WatchAdd:
			if liveErr==nil&&cmd.MaxItems<=live.MaxItems{return nil}
		}
		return managedqa.ErrFixture
	}
	dispatch:=q.PrivateDispatch(authorize,notify)
	s,err:=workerproto.OpenSession(ctx,r,w,workerproto.Worker,func(c context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
		if liveErr==nil&&op==workerproto.PTRTags{return managedPTRTags(c,live.PolicyConfig,body)}
		return dispatch(c,op,body)
	});if err!=nil{return err}
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
	if len(args)==1&&args[0]=="internal-managed-check-blacklist-v1"{
		if err:=checkManagedBlacklist();err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(2)}
		return true
	}
	if len(args)!=0&&args[0]=="internal-managed-policy-v1"{
		if err:=runManagedPolicy(args[1:]);err!=nil{fmt.Fprintln(os.Stderr,"managed source policy:",err);os.Exit(2)}
		return true
	}
	if len(args)==0||args[0]!="internal-managed-window-worker-v1"{return false}
	if err:=runManagedWindowWorker(args[1:]);err!=nil{fmt.Fprintln(os.Stderr,"managed window worker:",err);os.Exit(2)}
	return true
}
