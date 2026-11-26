package processguard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Two anonymous pipes, exactly the broker's intended transport. The
// factory can only create fixed test children.
func openProbeFixture(t *testing.T,ctx context.Context,set *OperationSet,start ProbeStarter)(*workerproto.Session,*workerproto.ProbeClient,<-chan error){
	t.Helper()
	return openVersionProbeFixture(t,ctx,set,start,nil)
}
func openVersionProbeFixture(t *testing.T,ctx context.Context,set *OperationSet,start ProbeStarter,version VersionProbeStarter)(*workerproto.Session,*workerproto.ProbeClient,<-chan error){
	t.Helper()
	cr,cw,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	sr,sw,err:=os.Pipe()
	if err!=nil{_ = cr.Close();_ = cw.Close();t.Fatal(err)}
	t.Cleanup(func(){_ = cr.Close();_ = cw.Close();_ = sr.Close();_ = sw.Close()})
	done:=make(chan error,1)
	go func(){done<-ServeProbeBrokerWithVersion(ctx,sr,cw,set,start,version)}()
	s,err:=workerproto.OpenSession(ctx,cr,sw,workerproto.OperationRequester,nil)
	if err!=nil{t.Fatal(err)}
	t.Cleanup(s.Close)
	client,err:=workerproto.NewProbeClient(s)
	if err!=nil{t.Fatal(err)}
	return s,client,done
}
func waitProbeCleanup(t *testing.T,ctx context.Context,done <-chan error)error{
	t.Helper()
	select{case err:=<-done:return err;case <-ctx.Done():t.Fatal(ctx.Err());return ctx.Err()}
}
func TestProbeBrokerPrivateStartStopAndChannelLoss(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	var mu sync.Mutex
	var children []*operationFixture
	s,c,done:=openProbeFixture(t,ctx,set,func(context.Context)(OperationChild,error){
		child:=new(operationFixture)
		mu.Lock();children=append(children,child);mu.Unlock()
		return child,nil
	})
	// Strict broker refuses command-shaped payloads before launch.
	e,err:=s.Call(ctx,workerproto.OperationProbeStart,json.RawMessage(`{"command":"fixture"}`))
	if err!=nil||e.Error==nil||e.Error.Code!="invalid"{t.Fatalf("invalid: %+v %v",e,err)}
	if _,err:=s.Call(ctx,workerproto.QueueEnqueue,nil);!errors.Is(err,workerproto.ErrDirection){t.Fatal(err)}
	a,err:=c.Start(ctx)
	if err!=nil{t.Fatal(err)}
	b,err:=c.Start(ctx)
	if err!=nil||a.ID==b.ID{t.Fatalf("second start: %v",err)}
	mu.Lock()
	if len(children)!=2{mu.Unlock();t.Fatal("unexpected launch")}
	first,second:=children[0],children[1]
	mu.Unlock()
	if err:=c.Stop(ctx,a);err!=nil{t.Fatal(err)}
	if !first.stopped.Load()||second.stopped.Load(){t.Fatal("individual stop affected sibling")}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
	if !second.stopped.Load(){t.Fatal("peer loss left operation")}
	if _,err:=set.Start(ctx,startOperationFixture(new(operationFixture)));!errors.Is(err,ErrOperationsSealed){t.Fatal(err)}
}

func TestProbeBrokerLostStartResponseSealsOperations(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(1)
	child:=new(operationFixture)
	entered,release:=make(chan struct{}),make(chan struct{})
	defer func(){select{case <-release:default:close(release)}}()
	_,c,done:=openProbeFixture(t,ctx,set,func(context.Context)(OperationChild,error){
		close(entered)
		<-release
		return child,nil
	})
	call,stop:=context.WithCancel(ctx)
	defer stop()
	result:=make(chan error,1)
	go func(){_,err:=c.Start(call);result<-err}()
	select{case <-entered:case <-ctx.Done():t.Fatal(ctx.Err())}
	stop()
	if err:=<-result;!errors.Is(err,context.Canceled){t.Fatalf("start: %v",err)}
	close(release)
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
	if !child.stopped.Load(){t.Fatal("ambiguous start leaked ownership")}
}

func TestProbeBrokerUnconfirmedCleanupIsNotSuccess(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(1)
	child:=new(operationFixture)
	child.fail.Store(true)
	s,c,done:=openProbeFixture(t,ctx,set,startOperationFixture(child))
	ref,err:=c.Start(ctx)
	if err!=nil{t.Fatal(err)}
	if err:=c.Stop(ctx,ref);!errors.Is(err,workerproto.ErrProbeBrokerRefused){t.Fatal(err)}
	select{case <-s.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
	if err:=waitProbeCleanup(t,ctx,done);!errors.Is(err,ErrOperationUnconfirmed){t.Fatalf("uncertain cleanup: %v",err)}
	child.fail.Store(false)
	if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
}

func TestProbeBrokerRevokesOnUnconfirmedCleanupWithoutClientCooperation(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	bad,other:=new(operationFixture),new(operationFixture)
	bad.fail.Store(true)
	next:=0
	s,c,done:=openProbeFixture(t,ctx,set,func(context.Context)(OperationChild,error){
		next++
		if next==1{return bad,nil}
		return other,nil
	})
	ref,err:=c.Start(ctx)
	if err!=nil{t.Fatal(err)}
	if _,err:=c.Start(ctx);err!=nil{t.Fatal(err)}
	body,_:=json.Marshal(ref)
	// Raw requester deliberately does not close on the refused stop.
	e,err:=s.Call(ctx,workerproto.OperationProbeStop,body)
	if err==nil&&e.Error==nil{t.Fatal("uncertain stop acknowledged")}
	select{case <-s.Done():case <-ctx.Done():t.Fatal("broker did not independently revoke")}
	if err:=waitProbeCleanup(t,ctx,done);!errors.Is(err,ErrOperationUnconfirmed){t.Fatalf("uncertain cleanup: %v",err)}
	if !other.stopped.Load(){t.Fatal("sibling left active after failed cleanup")}
	bad.fail.Store(false)
	if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
}

type versionProbeFixture struct{
	operationFixture
	value string
	read func(context.Context)(string,error)
	beforeStop func(context.Context)error
}
func(f *versionProbeFixture)Version(ctx context.Context)(string,error){
	if f.read!=nil{return f.read(ctx)}
	return f.value,nil
}
func(f *versionProbeFixture)Stop(ctx context.Context)error{
	if f.beforeStop!=nil{if err:=f.beforeStop(ctx);err!=nil{return err}}
	return f.operationFixture.Stop(ctx)
}
func TestVersionBrokerWaitsForCleanupAndPreservesSibling(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	sibling:=new(operationFixture)
	entered,release:=make(chan struct{}),make(chan struct{})
	defer func(){select{case <-release:default:close(release)}}()
	child:=&versionProbeFixture{value:"1.32.20",beforeStop:func(ctx context.Context)error{
		close(entered)
		select{case <-release:return nil;case <-ctx.Done():return ctx.Err()}
	}}
	starts:=0
	s,c,done:=openVersionProbeFixture(t,ctx,set,startOperationFixture(sibling),func(context.Context)(VersionProbeChild,error){starts++;return child,nil})
	e,err:=s.Call(ctx,workerproto.OperationVersionProbe,json.RawMessage(`{"argv":["--version"]}`))
	if err!=nil||e.Error==nil||e.Error.Code!="invalid"{t.Fatalf("invalid: %+v %v",e,err)}
	ref,err:=c.Start(ctx)
	if err!=nil{t.Fatal(err)}
	type answer struct{v workerproto.VersionProbeResult;err error}
	result:=make(chan answer,1)
	go func(){v,err:=c.Version(ctx);result<-answer{v,err}}()
	select{case <-entered:case <-ctx.Done():t.Fatal(ctx.Err())}
	select{case got:=<-result:t.Fatalf("early result: %+v",got);default:}
	if sibling.stopped.Load(){t.Fatal("sibling stopped")}
	close(release)
	select{
	case got:=<-result:if got.err!=nil||got.v.Version!="1.32.20"||!child.stopped.Load(){t.Fatalf("result: %+v",got)}
	case <-ctx.Done():t.Fatal(ctx.Err())
	}
	if starts!=1||sibling.stopped.Load(){t.Fatal("unexpected start or sibling termination")}
	if err:=c.Stop(ctx,ref);err!=nil{t.Fatal(err)}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
}
func TestVersionBrokerUnconfirmedCleanupRevokesRawRequester(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	sibling:=new(operationFixture)
	child:=&versionProbeFixture{value:"1.32.19"}
	child.fail.Store(true)
	s,c,done:=openVersionProbeFixture(t,ctx,set,startOperationFixture(sibling),func(context.Context)(VersionProbeChild,error){return child,nil})
	if _,err:=c.Start(ctx);err!=nil{t.Fatal(err)}
	e,err:=s.Call(ctx,workerproto.OperationVersionProbe,nil)
	if err==nil&&e.Error==nil{t.Fatal("unconfirmed runtime acknowledged")}
	select{case <-s.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
	if err:=waitProbeCleanup(t,ctx,done);!errors.Is(err,ErrOperationUnconfirmed){t.Fatal(err)}
	if !sibling.stopped.Load(){t.Fatal("sibling left after uncertainty")}
	child.fail.Store(false)
	if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
}
func TestVersionBrokerCanceledCallStillCleansRuntime(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(1)
	entered:=make(chan struct{})
	child:=&versionProbeFixture{read:func(ctx context.Context)(string,error){
		close(entered)
		<-ctx.Done()
		return "",ctx.Err()
	}}
	s,c,done:=openVersionProbeFixture(t,ctx,set,startOperationFixture(new(operationFixture)),func(context.Context)(VersionProbeChild,error){return child,nil})
	call,stop:=context.WithCancel(ctx)
	defer stop()
	result:=make(chan error,1)
	go func(){_,err:=c.Version(call);result<-err}()
	select{case <-entered:case <-ctx.Done():t.Fatal(ctx.Err())}
	stop()
	if err:=<-result;!errors.Is(err,context.Canceled){t.Fatal(err)}
	select{case <-s.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
	if !child.stopped.Load(){t.Fatal("canceled version left process")}
}
