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
	cr,cw,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	sr,sw,err:=os.Pipe()
	if err!=nil{_ = cr.Close();_ = cw.Close();t.Fatal(err)}
	t.Cleanup(func(){_ = cr.Close();_ = cw.Close();_ = sr.Close();_ = sw.Close()})
	done:=make(chan error,1)
	go func(){done<-ServeProbeBroker(ctx,sr,cw,set,start)}()
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
