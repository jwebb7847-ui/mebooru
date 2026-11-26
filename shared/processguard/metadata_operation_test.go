package processguard

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

const fixtureClassification = "Category / Subcategory / Site\n\"derpibooru\" / \"post\" / \"Derpibooru\"\n"
const fixtureMetadata = `[[3,"https://example.invalid/fixture",{"id":1,"category":"derpibooru","subcategory":"post","tags":["fixture"]}]]`

func metadataGrant(t *testing.T)(workerproto.MetadataPlanRequest,acquisitionplan.RuntimeIdentity,acquisitionplan.MetadataConfig,acquisitionplan.MetadataIdentity){
	t.Helper()
	q:=workerproto.MetadataPlanRequest{JobID:7,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/images/1",First:1,Last:1}
	r:=acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:strings.Repeat("a",64),ManifestSHA256:strings.Repeat("b",64)}
	c:=acquisitionplan.MetadataConfig{Generation:1,Source:q.Source}
	ci,err:=c.Identity()
	if err!=nil{t.Fatal(err)}
	return q,r,c,acquisitionplan.MetadataIdentity{Runtime:r,Config:ci}
}

type metadataChildFixture struct{
	operationFixture
	data string
	readErr error
	exit int
	stderr []byte
	read func(context.Context)([]byte,bool,error)
	stop func(context.Context)error
}
func(c *metadataChildFixture)ReadChunk(ctx context.Context)([]byte,bool,error){
	if c.read!=nil{return c.read(ctx)}
	return []byte(c.data),true,c.readErr
}
func(c *metadataChildFixture)Result(context.Context)(int,[]byte,error){return c.exit,c.stderr,nil}
func(c *metadataChildFixture)Stop(ctx context.Context)error{
	if c.stop!=nil{return c.stop(ctx)}
	return c.operationFixture.Stop(ctx)
}
func TestMetadataOperationExactGrantAndCleanup(t *testing.T){
	ctx:=context.Background()
	q,r,c,id:=metadataGrant(t)
	set:=NewOperationSet(1)
	gate:=workerproto.NewDrainGate(ctx)
	var first,second *metadataChildFixture
	var calls int
	makeOp:=func()*MetadataOperation{
		o,err:=NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},func(_ context.Context,kind workerproto.MetadataKind,args []string)(StreamProbeChild,error){
			calls++
			if args[0]!="--config-ignore"||args[len(args)-1]!=q.URL{t.Fatalf("unexpected args: %v",args)}
			switch kind{
			case workerproto.MetadataClassify:
				first=&metadataChildFixture{data:fixtureClassification}
				return first,nil
			case workerproto.MetadataStrictResolve:
				if first==nil||!first.stopped.Load(){t.Fatal("resolve started before classification cleanup")}
				second=&metadataChildFixture{data:fixtureMetadata}
				return second,nil
			default:t.Fatal("unknown operation")
			}
			return nil,ErrMetadataOperation
		})
		if err!=nil{t.Fatal(err)}
		return o
	}
	o:=makeOp()
	if out,err:=o.Run(ctx,q);out!=nil||!errors.Is(err,workerproto.ErrOffline)||calls!=0{t.Fatal("offline admission")}
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	if out,err:=o.Run(ctx,q);out!=nil||err==nil{t.Fatal("offline grant replay")}
	o=makeOp()
	bad:=q;bad.Gallery="B"
	if out,err:=o.Run(ctx,bad);out!=nil||err==nil||calls!=0{t.Fatal("widened grant")}
	out,err:=o.Run(ctx,q)
	if err!=nil||string(out)!=fixtureMetadata||calls!=2||second==nil||!second.stopped.Load(){t.Fatalf("metadata result %s %v",out,err)}
	if _,err:=o.Run(ctx,q);err==nil{t.Fatal("replay")}
	if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
	if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
}
func TestMetadataOperationFailuresNeverPublish(t *testing.T){
	for _,tc:=range []struct{
		name string
		change func(*metadataChildFixture)
		mutateIdentity bool
		cleanupUncertain bool
	}{
		{"bad classification",func(c *metadataChildFixture){c.data="bad"},false,false},
		{"exit",func(c *metadataChildFixture){c.exit=1},false,false},
		{"stderr",func(c *metadataChildFixture){c.stderr=[]byte("secret")},false,false},
		{"oversize chunk",func(c *metadataChildFixture){c.data=strings.Repeat("x",workerproto.StreamChunkLimit+1)},false,false},
		{"read error",func(c *metadataChildFixture){c.readErr=errors.New("secret")},false,false},
		{"changed runtime",func(*metadataChildFixture){},true,false},
		{"cleanup failure",func(c *metadataChildFixture){c.fail.Store(true)},false,true},
	}{
		t.Run(tc.name,func(t *testing.T){
			ctx:=context.Background()
			q,r,c,id:=metadataGrant(t)
			set:=NewOperationSet(1)
			gate:=workerproto.NewDrainGate(ctx)
			if err:=gate.Open();err!=nil{t.Fatal(err)}
			child:=&metadataChildFixture{data:fixtureClassification}
			tc.change(child)
			calls,observations:=0,0
			o,err:=NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){
				observations++
				actual:=id
				if tc.mutateIdentity&&observations>1{actual.Runtime.Generation++}
				return actual,nil
			},func(context.Context,workerproto.MetadataKind,[]string)(StreamProbeChild,error){calls++;return child,nil})
			if err!=nil{t.Fatal(err)}
			out,err:=o.Run(ctx,q)
			if out!=nil||err==nil||strings.Contains(err.Error(),"secret")||calls!=1{t.Fatalf("failure published %q %v %d",out,err,calls)}
			if tc.cleanupUncertain{
				if !errors.Is(err,ErrOperationUnconfirmed)||!errors.Is(gate.Drain(ctx),workerproto.ErrDrainUnconfirmed)||gate.Open()==nil{t.Fatal("cleanup uncertainty lost")}
				child.fail.Store(false)
			}else if !child.stopped.Load(){t.Fatal("cleanup not performed")}
			if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
		})
	}
}
func TestMetadataOperationDrainSpansBothPhases(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
	defer cancel()
	q,r,c,id:=metadataGrant(t)
	set:=NewOperationSet(1)
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	entered,revoked,stopEntered,release:=make(chan struct{}),make(chan struct{}),make(chan struct{}),make(chan struct{})
	var starts atomic.Int32
	child:=&metadataChildFixture{}
	child.read=func(ctx context.Context)([]byte,bool,error){
		close(entered)
		<-ctx.Done()
		close(revoked)
		return nil,false,ctx.Err()
	}
	child.stop=func(ctx context.Context)error{
		close(stopEntered)
		select{case <-release:return nil;case <-ctx.Done():return ctx.Err()}
	}
	o,err:=NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},
		func(context.Context,workerproto.MetadataKind,[]string)(StreamProbeChild,error){starts.Add(1);return child,nil})
	if err!=nil{t.Fatal(err)}
	run:=make(chan error,1)
	go func(){out,err:=o.Run(ctx,q);if out!=nil{run<-errors.New("published canceled output");return};run<-err}()
	wait:=func(ch <-chan struct{}){t.Helper();select{case <-ch:case <-ctx.Done():t.Fatal(ctx.Err())}}
	wait(entered)
	drained:=make(chan error,1)
	go func(){drained<-gate.Drain(ctx)}()
	wait(revoked);wait(stopEntered)
	if err:=gate.Open();!errors.Is(err,workerproto.ErrDrainPending){t.Fatalf("reopened during cleanup: %v",err)}
	select{case err:=<-drained:t.Fatalf("drain acknowledged before cleanup: %v",err);default:}
	close(release)
	select{case err:=<-drained:if err!=nil{t.Fatal(err)};case <-ctx.Done():t.Fatal(ctx.Err())}
	select{case err:=<-run:if !errors.Is(err,ErrMetadataOperation){t.Fatal(err)};case <-ctx.Done():t.Fatal(ctx.Err())}
	if starts.Load()!=1{t.Fatal("resolve started after revocation")}
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	if _,err:=o.Run(ctx,q);err==nil{t.Fatal("old generation revived")}
	if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
}

func TestMetadataOperationResolveFailureNeverPublishes(t *testing.T){
	for _,tc:=range []struct{name string; change func(*metadataChildFixture)}{
		{"non JSON",func(c *metadataChildFixture){c.data="invalid"}},
		{"wrong JSON root",func(c *metadataChildFixture){c.data="{}"}},
		{"nonzero exit",func(c *metadataChildFixture){c.exit=1}},
		{"read failure",func(c *metadataChildFixture){c.readErr=errors.New("secret")}},
		{"cleanup uncertainty",func(c *metadataChildFixture){c.fail.Store(true)}},
	}{
		t.Run(tc.name,func(t *testing.T){
			ctx:=context.Background()
			q,r,c,id:=metadataGrant(t)
			set:=NewOperationSet(1)
			gate:=workerproto.NewDrainGate(ctx)
			if err:=gate.Open();err!=nil{t.Fatal(err)}
			first:=&metadataChildFixture{data:fixtureClassification}
			second:=&metadataChildFixture{data:fixtureMetadata}
			tc.change(second)
			o,err:=NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},
				func(_ context.Context,kind workerproto.MetadataKind,_ []string)(StreamProbeChild,error){
					if kind==workerproto.MetadataClassify{return first,nil}
					if !first.stopped.Load(){t.Fatal("classification not cleaned")}
					return second,nil
				})
			if err!=nil{t.Fatal(err)}
			out,err:=o.Run(ctx,q)
			if out!=nil||err==nil||strings.Contains(err.Error(),"secret")||!second.armed.Load(){t.Fatalf("resolve failure %q %v",out,err)}
			if second.fail.Load(){
				if !errors.Is(err,ErrOperationUnconfirmed)||!errors.Is(gate.Drain(ctx),workerproto.ErrDrainUnconfirmed){t.Fatal("lost cleanup uncertainty")}
				second.fail.Store(false)
			}else if !second.stopped.Load(){t.Fatal("resolve child not cleaned")}
			if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
		})
	}
}

func TestMetadataOperationCanceledBetweenStages(t *testing.T){
	ctx,cancel:=context.WithCancel(context.Background())
	defer cancel()
	q,r,c,id:=metadataGrant(t)
	set:=NewOperationSet(1)
	gate:=workerproto.NewDrainGate(context.Background())
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	child:=&metadataChildFixture{data:fixtureClassification}
	observations,starts:=0,0
	o,err:=NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){
		observations++
		if observations==2{cancel()}
		return id,nil
	},func(context.Context,workerproto.MetadataKind,[]string)(StreamProbeChild,error){starts++;return child,nil})
	if err!=nil{t.Fatal(err)}
	if out,err:=o.Run(ctx,q);out!=nil||err==nil||starts!=1||!child.stopped.Load(){t.Fatalf("cancellation between phases: %q %v %d",out,err,starts)}
	if err:=gate.Drain(context.Background());err!=nil{t.Fatal(err)}
	if err:=set.SealAndStop(context.Background());err!=nil{t.Fatal(err)}
}
