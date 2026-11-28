package main

import(
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"slices"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/managedqa"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

// Disposable executable fixture. The independent custodian, not the
// worker, compiles and starts the exact six runtime operations.
func runManagedWindowCustody(dir,generation,token string)error{
	live,liveErr:=windowregistry.LoadLive(dir)
	var setup managedqa.Setup
	if errors.Is(liveErr,os.ErrNotExist){
		var err error
		setup,err=managedqa.Load(dir);if err!=nil{return err}
	}else if liveErr!=nil{return liveErr}
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	r,w,err:=processguard.OpenInheritedPipes(token);if err!=nil{return err}
	defer r.Close();defer w.Close()
	store,err:=acquisitionpolicy.Open(dir);if err!=nil{return err};defer store.Close()
	var ctx context.Context
	var cancel context.CancelFunc
	if setup.Application || liveErr==nil {
		ctx, cancel = context.WithCancel(context.Background())
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), 12*time.Minute)
	}
	defer cancel()
	current:=func(c context.Context)bool{
		off,err:=store.Load()
		_,revoked:=os.Lstat(filepath.Join(dir,managedqa.RevokedName))
		return c.Err()==nil&&err==nil&&!off&&errors.Is(revoked,os.ErrNotExist)
	}
	registry,err:=windowregistry.Open(filepath.Join(dir,"windows"));if err!=nil{return err};defer registry.Close()
	runtime,config,_:=setup.Identity()
	makeGrant:=func(md workerproto.MetadataPlanRequest)processguard.ManagedRuntimeGrant{
		if liveErr==nil{return liveRuntimeGrant(dir,generation,live,md,current)}
		return processguard.ManagedRuntimeGrant{
		Metadata:md,Runtime:runtime,Config:config,Current:current,
		MetadataComplete:func(c context.Context,q workerproto.MetadataPlanRequest,sub string,b []byte)error{
			if !current(c)||q!=md||sub!="search"{return managedqa.ErrFixture}
			return managedqa.Write(dir,managedqa.JobFile(managedqa.MetadataName,md.JobID),b)
		},
		Transfer:func(c context.Context)(workerproto.StageAllocation,string,error){
			var g managedqa.Grant
			if !current(c)||managedqa.ReadJSON(dir,managedqa.JobFile(managedqa.GrantName,md.JobID),&g,64<<10)!=nil{return workerproto.StageAllocation{},"",managedqa.ErrFixture}
			path,err:=managedqa.Workspace(dir,g.Allocation)
			return g.Allocation,path,err
		},
		Start:func(c context.Context,args []string)(processguard.StreamProbeChild,error){
			if !current(c){return nil,managedqa.ErrFixture}
			return startManagedWindowRuntime(c,dir,args)
		},
		Complete:func(c context.Context,g workerproto.StageAllocation,files []acquisitionplan.VerifiedTransferFile)error{
			var known managedqa.Grant
			if !current(c)||managedqa.ReadJSON(dir,managedqa.JobFile(managedqa.GrantName,md.JobID),&known,64<<10)!=nil||known.Allocation!=g{return managedqa.ErrFixture}
			return managedqa.WriteJSON(dir,managedqa.JobFile(managedqa.ProofName,md.JobID),managedqa.Proof{Allocation:g,Files:files})
		},
	}}
	resolver:=&managedWindowResolver{registry:registry,generation:generation,setup:setup,makeGrant:makeGrant,live:liveErr==nil,dir:dir}
	return acquisitionpolicy.RunCustodianReady(ctx,dir,generation,os.Stdin,os.Stdout,func(start context.Context)(acquisitionpolicy.CustodyChild,error){
		if err:=start.Err();err!=nil{return nil,err}
		set:=processguard.NewOperationSet(1)
		br,bw,err:=os.Pipe();if err!=nil{return nil,err}
		rr,rw,err:=os.Pipe();if err!=nil{br.Close();bw.Close();return nil,err}
		primary,err:=startManagedWindowWorker(r,w,br,rw,dir)
		r.Close();w.Close();br.Close();rw.Close()
		if primary==nil{rr.Close();bw.Close();return nil,err}
		brokerCtx,stop:=context.WithCancel(ctx)
		done:=make(chan struct{})
		owned:=&probeCustody{primary:primary,set:set,stopBroker:stop,brokerDone:done,done:make(chan struct{})}
		go func(){owned.brokerErr=processguard.ServeManagedRuntimeWindows(brokerCtx,rr,bw,set,resolver);close(done)}()
		go func(){select{case <-primary.Done():case <-done:};close(owned.done)}()
		return owned,err
	})
}

func startManagedWindowRuntime(ctx context.Context,dir string,args []string)(processguard.StreamProbeChild,error){
	if ctx.Err()!=nil||len(args)==0||len(args)>128||args[0]!="--config-ignore"{return nil,managedqa.ErrFixture}
	raw,err:=json.Marshal(args);if err!=nil||len(raw)>12<<10{return nil,managedqa.ErrFixture}
	return startWindowRuntimeCommand(dir,raw)
}

func startWindowRuntimeCommand(dir string,raw []byte)(processguard.StreamProbeChild,error){
	if len(raw)>48<<10{return nil,windowregistry.ErrWindow}
	encoded:=base64.RawURLEncoding.EncodeToString(raw)
	r,w,err:=os.Pipe();if err!=nil{return nil,err};defer w.Close()
	s,t,err:=os.Pipe();if err!=nil{r.Close();return nil,err};defer t.Close()
	child,err:=startManagedWindowRuntimePlatform(w,t,dir,encoded)
	if child==nil{r.Close();s.Close();return nil,err}
	return &runtimeStreamOperation{OperationChild:child,stdout:r,status:s,result:make(chan runtimeStreamResult,1)},err
}

func runManagedWindowRuntimeLeaf(dir,encoded string)error{
	if cfg,err:=windowregistry.LoadLive(dir);err==nil{
		return runLiveRuntimeLeaf(dir,encoded,cfg)
	}else if !errors.Is(err,os.ErrNotExist){return err}
	setup,err:=managedqa.Load(dir);if err!=nil{return err}
	raw,err:=base64.RawURLEncoding.DecodeString(encoded);if err!=nil||len(raw)>12<<10{return managedqa.ErrFixture}
	var args []string
	if json.Unmarshal(raw,&args)!=nil||len(args)==0||len(args)>128||args[0]!="--config-ignore"{return managedqa.ErrFixture}
	if !validWindowFixtureArgs(dir,setup,args){return managedqa.ErrFixture}
	cmd:=exec.Command(setup.Python,append([]string{filepath.Join(dir,managedqa.ScriptName)},args...)...)
	cmd.Dir=dir
	cmd.Env=append(os.Environ(),"MEBOORU_WINDOW_PORT="+strconv.Itoa(setup.Port),"MEBOORU_WINDOW_COUNT="+strconv.Itoa(setup.Count),"MEBOORU_WINDOW_WINDOWS="+strconv.Itoa(setup.Windows))
	cmd.Env=append(cmd.Env,"MEBOORU_WINDOW_CANCEL_FIRST="+strconv.FormatBool(setup.CancelFirst))
	cmd.Stdout=os.Stdout
	var stderr probeOutput
	cmd.Stderr=&stderr
	err=cmd.Run()
	result:=runtimeVersionResult{Started:cmd.Process!=nil,Overflow:stderr.overflow,Stderr:stderr.Bytes()}
	if err!=nil{
		var exit *exec.ExitError
		if errors.As(err,&exit){result.ExitCode=exit.ExitCode()}else{result.Failed=true}
	}
	return json.NewEncoder(os.Stderr).Encode(result)
}

// The fixture leaf also refuses command shapes outside its exact known
// job. Its argv originates from the custodian, not the private worker.
func validWindowFixtureArgs(dir string,setup managedqa.Setup,args []string)bool{
	for job:=int64(1);job<=int64(setup.Windows);job++{
	q:=setup.MetadataFor(job)
	for _,kind:=range []workerproto.MetadataKind{workerproto.MetadataClassify,workerproto.MetadataStrictResolve}{
		v:=q;v.Kind=kind;if kind==workerproto.MetadataClassify{v.First,v.Last=0,0}
		compiled,err:=acquisitionplan.CompileMetadata(v,v)
		if err==nil{
			expected:=append([]string{"--config-ignore","-o","timeout=20","-o","retries=2"},compiled[1:]...)
			if slices.Equal(expected,args){return true}
		}
	}
	var grant managedqa.Grant
	if managedqa.ReadJSON(dir,managedqa.JobFile(managedqa.GrantName,job),&grant,64<<10)!=nil||grant.Allocation.Request.Transfer.Metadata!=q{continue}
	path,err:=managedqa.Workspace(dir,grant.Allocation);if err!=nil{continue}
	_,cfg,_:=setup.Identity()
	cmd,err:=acquisitionplan.ObservePreparedTransferCommands(grant.Allocation.Request.Transfer,cfg,path)
	if err==nil&&(slices.Equal(cmd.Version,args)||slices.Equal(cmd.Negative,args)||slices.Equal(cmd.Positive,args)||slices.Equal(cmd.Transfer,args)){return true}
	}
	return false
}


type managedWindowResolver struct{
	registry *windowregistry.Registry
	generation string
	setup managedqa.Setup
	makeGrant func(workerproto.MetadataPlanRequest)processguard.ManagedRuntimeGrant
	active windowregistry.Grant
	live bool
	dir string
}
func(r *managedWindowResolver)Claim(c context.Context,q workerproto.MetadataPlanRequest)(processguard.ManagedRuntimeGrant,error){
	if (!r.live&&q!=r.setup.MetadataFor(q.JobID))||!workerproto.ValidMetadataPlan(q){return processguard.ManagedRuntimeGrant{},managedqa.ErrFixture}
	grant:=r.makeGrant(q)
	if !grant.Current(c){return processguard.ManagedRuntimeGrant{},managedqa.ErrFixture}
	observed,err:=r.registry.Observe(q,r.generation);if err!=nil{return processguard.ManagedRuntimeGrant{},err}
	ci,err:=grant.Config.Identity();if err!=nil||observed.Identity!=(acquisitionplan.MetadataIdentity{Runtime:grant.Runtime,Config:ci})||observed.PolicyGeneration!=1{return processguard.ManagedRuntimeGrant{},managedqa.ErrFixture}
	g,err:=r.registry.Claim(c,q,r.generation);if err!=nil||g!=observed{return processguard.ManagedRuntimeGrant{},managedqa.ErrFixture}
	r.active=g
	v:=grant
	v.Current=func(c context.Context)bool{return grant.Current(c)&&r.registry.Admitting(g)}
	return v,nil
}
func(r *managedWindowResolver)Settle(c context.Context,q workerproto.MetadataPlanRequest,phase int,failed bool)error{
	if q!=r.active.Request{return managedqa.ErrFixture}
	// Close arrives after the broker has stopped all runtime handles.
	// Before metadata completion this window could not allocate any
	// workspace or publish gallery data. It is safe to retire it and
	// let the next independently authorized job use the same worker.
	if (failed && phase < 2 || r.live && phase < 6) && r.registry.Admitting(r.active) {
		if err := r.registry.RecordOutcome(c, windowregistry.Outcome{Grant:r.active,Kind:"abandoned"}); err != nil { return err }
	}
	return r.registry.Settle(c,r.active,phase,failed)
}
