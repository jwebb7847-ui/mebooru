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
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

// Disposable executable fixture. The independent custodian, not the
// worker, compiles and starts the exact six runtime operations.
func runManagedWindowCustody(dir,generation,token string)error{
	setup,err:=managedqa.Load(dir);if err!=nil{return err}
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	r,w,err:=processguard.OpenInheritedPipes(token);if err!=nil{return err}
	defer r.Close();defer w.Close()
	store,err:=acquisitionpolicy.Open(dir);if err!=nil{return err};defer store.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),12*time.Minute);defer cancel()
	current:=func(c context.Context)bool{
		off,err:=store.Load()
		_,revoked:=os.Lstat(filepath.Join(dir,managedqa.RevokedName))
		return c.Err()==nil&&err==nil&&!off&&errors.Is(revoked,os.ErrNotExist)
	}
	runtime,config,_:=setup.Identity()
	grant:=processguard.ManagedRuntimeGrant{
		Metadata:setup.Metadata(),Runtime:runtime,Config:config,Current:current,
		MetadataComplete:func(c context.Context,q workerproto.MetadataPlanRequest,sub string,b []byte)error{
			if !current(c)||q!=setup.Metadata()||sub!="search"{return managedqa.ErrFixture}
			return managedqa.Write(dir,managedqa.MetadataName,b)
		},
		Transfer:func(c context.Context)(workerproto.StageAllocation,string,error){
			var g managedqa.Grant
			if !current(c)||managedqa.ReadJSON(dir,managedqa.GrantName,&g,64<<10)!=nil{return workerproto.StageAllocation{},"",managedqa.ErrFixture}
			path,err:=managedqa.Workspace(dir,g.Allocation)
			return g.Allocation,path,err
		},
		Start:func(c context.Context,args []string)(processguard.StreamProbeChild,error){
			if !current(c){return nil,managedqa.ErrFixture}
			return startManagedWindowRuntime(c,dir,args)
		},
		Complete:func(c context.Context,g workerproto.StageAllocation,files []acquisitionplan.VerifiedTransferFile)error{
			var known managedqa.Grant
			if !current(c)||managedqa.ReadJSON(dir,managedqa.GrantName,&known,64<<10)!=nil||known.Allocation!=g{return managedqa.ErrFixture}
			return managedqa.WriteJSON(dir,managedqa.ProofName,managedqa.Proof{Allocation:g,Files:files})
		},
	}
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
		go func(){owned.brokerErr=processguard.ServeManagedRuntimeBroker(brokerCtx,rr,bw,set,grant);close(done)}()
		go func(){select{case <-primary.Done():case <-done:};close(owned.done)}()
		return owned,err
	})
}

func startManagedWindowRuntime(ctx context.Context,dir string,args []string)(processguard.StreamProbeChild,error){
	if ctx.Err()!=nil||len(args)==0||len(args)>128||args[0]!="--config-ignore"{return nil,managedqa.ErrFixture}
	raw,err:=json.Marshal(args);if err!=nil||len(raw)>12<<10{return nil,managedqa.ErrFixture}
	encoded:=base64.RawURLEncoding.EncodeToString(raw)
	r,w,err:=os.Pipe();if err!=nil{return nil,err};defer w.Close()
	s,t,err:=os.Pipe();if err!=nil{r.Close();return nil,err};defer t.Close()
	child,err:=startManagedWindowRuntimePlatform(w,t,dir,encoded)
	if child==nil{r.Close();s.Close();return nil,err}
	return &runtimeStreamOperation{OperationChild:child,stdout:r,status:s,result:make(chan runtimeStreamResult,1)},err
}

func runManagedWindowRuntimeLeaf(dir,encoded string)error{
	setup,err:=managedqa.Load(dir);if err!=nil{return err}
	raw,err:=base64.RawURLEncoding.DecodeString(encoded);if err!=nil||len(raw)>12<<10{return managedqa.ErrFixture}
	var args []string
	if json.Unmarshal(raw,&args)!=nil||len(args)==0||len(args)>128||args[0]!="--config-ignore"{return managedqa.ErrFixture}
	if !validWindowFixtureArgs(dir,setup,args){return managedqa.ErrFixture}
	cmd:=exec.Command(setup.Python,append([]string{filepath.Join(dir,managedqa.ScriptName)},args...)...)
	cmd.Dir=dir
	cmd.Env=append(os.Environ(),"MEBOORU_WINDOW_PORT="+strconv.Itoa(setup.Port),"MEBOORU_WINDOW_COUNT="+strconv.Itoa(setup.Count))
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
	q:=setup.Metadata()
	for _,kind:=range []workerproto.MetadataKind{workerproto.MetadataClassify,workerproto.MetadataStrictResolve}{
		v:=q;v.Kind=kind;if kind==workerproto.MetadataClassify{v.First,v.Last=0,0}
		compiled,err:=acquisitionplan.CompileMetadata(v,v)
		if err==nil{
			expected:=append([]string{"--config-ignore","-o","timeout=20","-o","retries=2"},compiled[1:]...)
			if slices.Equal(expected,args){return true}
		}
	}
	var grant managedqa.Grant
	if managedqa.ReadJSON(dir,managedqa.GrantName,&grant,64<<10)!=nil||grant.Allocation.Request.Transfer.Metadata!=q{return false}
	path,err:=managedqa.Workspace(dir,grant.Allocation);if err!=nil{return false}
	_,cfg,_:=setup.Identity()
	cmd,err:=acquisitionplan.ObservePreparedTransferCommands(grant.Allocation.Request.Transfer,cfg,path)
	return err==nil&&(slices.Equal(cmd.Version,args)||slices.Equal(cmd.Negative,args)||slices.Equal(cmd.Positive,args)||slices.Equal(cmd.Transfer,args))
}
