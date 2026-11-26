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
	return openTransferProbeFixture(t,ctx,set,start,version,nil)
}
func openTransferProbeFixture(t *testing.T,ctx context.Context,set *OperationSet,start ProbeStarter,version VersionProbeStarter,transfer ProbeStarter)(*workerproto.Session,*workerproto.ProbeClient,<-chan error){
	t.Helper()
	cr,cw,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	sr,sw,err:=os.Pipe()
	if err!=nil{_ = cr.Close();_ = cw.Close();t.Fatal(err)}
	t.Cleanup(func(){_ = cr.Close();_ = cw.Close();_ = sr.Close();_ = sw.Close()})
	done:=make(chan error,1)
	go func(){done<-ServeProbeBrokerWithTransfer(ctx,sr,cw,set,start,version,nil,transfer)}()
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

func TestVersionBrokerDisabledAndFailedStart(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(1)
	s,_,done:=openProbeFixture(t,ctx,set,startOperationFixture(new(operationFixture)))
	e,err:=s.Call(ctx,workerproto.OperationVersionProbe,nil)
	if err!=nil||e.Error==nil||e.Error.Code!="unsupported"{t.Fatalf("disabled: %+v %v",e,err)}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}

	set=NewOperationSet(1)
	child:=&versionProbeFixture{read:func(context.Context)(string,error){
		t.Error("failed start reached version reader")
		return "",ErrOperationUnconfirmed
	}}
	s,c,done:=openVersionProbeFixture(t,ctx,set,startOperationFixture(new(operationFixture)),func(context.Context)(VersionProbeChild,error){
		return child,errors.New("dormant start failed")
	})
	e,err=s.Call(ctx,workerproto.OperationVersionProbe,nil)
	if err!=nil||e.Error==nil||e.Error.Code!="runtime_probe_failed"||!child.stopped.Load()||child.armed.Load(){t.Fatalf("failed start: %+v %v",e,err)}
	ref,err:=c.Start(ctx)
	if err!=nil{t.Fatal("failed dormant operation still occupies slot",err)}
	if err:=c.Stop(ctx,ref);err!=nil{t.Fatal(err)}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
}

func TestVersionBrokerRawCancellationPreservesSibling(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	sibling:=new(operationFixture)
	entered,cleanup:=make(chan struct{}),make(chan struct{})
	child:=&versionProbeFixture{read:func(ctx context.Context)(string,error){
		close(entered)
		<-ctx.Done()
		return "",ctx.Err()
	},beforeStop:func(context.Context)error{
		close(cleanup)
		return nil
	}}
	s,c,done:=openVersionProbeFixture(t,ctx,set,startOperationFixture(sibling),func(context.Context)(VersionProbeChild,error){return child,nil})
	ref,err:=c.Start(ctx)
	if err!=nil{t.Fatal(err)}
	call,stop:=context.WithCancel(ctx)
	defer stop()
	result:=make(chan error,1)
	go func(){_,err:=s.Call(call,workerproto.OperationVersionProbe,nil);result<-err}()
	select{case <-entered:case <-ctx.Done():t.Fatal(ctx.Err())}
	stop()
	if err:=<-result;!errors.Is(err,context.Canceled){t.Fatal(err)}
	select{case <-cleanup:case <-ctx.Done():t.Fatal(ctx.Err())}
	// Wait for actual lease removal (not just the cancellation request).
	tick:=time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for{
		set.mu.Lock()
		remaining:=len(set.children)
		set.mu.Unlock()
		if remaining==1{break}
		select{case <-ctx.Done():t.Fatal(ctx.Err());case <-tick.C:}
	}
	if !child.stopped.Load()||sibling.stopped.Load(){t.Fatal("individual cancellation affected sibling")}
	if err:=c.Stop(ctx,ref);err!=nil{t.Fatal(err)}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
}

func TestTransferProbeWorkerDrainWaitsForProof(t *testing.T){
	for _,fail:=range []bool{false,true}{
		name:="confirmed"
		if fail{name="unconfirmed"}
		t.Run(name,func(t *testing.T){
			ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
			defer cancel()
			entered,release:=make(chan struct{}),make(chan struct{})
			defer func(){select{case <-release:default:close(release)}}()
			var once sync.Once
			child:=&versionProbeFixture{beforeStop:func(ctx context.Context)error{
				once.Do(func(){close(entered)})
				select{case <-release:return nil;case <-ctx.Done():return ctx.Err()}
			}}
			child.fail.Store(fail)
			set:=NewOperationSet(2)
			b,c,done:=openTransferProbeFixture(t,ctx,set,func(context.Context)(OperationChild,error){return new(operationFixture),nil},nil,func(context.Context)(OperationChild,error){return child,nil})
			lr,lw,err:=os.Pipe();if err!=nil{t.Fatal(err)}
			wr,ww,err:=os.Pipe();if err!=nil{_ = lr.Close();_ = lw.Close();t.Fatal(err)}
			defer lr.Close();defer lw.Close();defer wr.Close();defer ww.Close()
			workerDone:=make(chan error,1)
			go func(){workerDone<-workerproto.ServeLifecycleProbeWithBroker(ctx,wr,lw,c)}()
			lib,err:=workerproto.OpenSession(ctx,lr,ww,workerproto.Library,nil)
			if err!=nil{t.Fatal(err)}
			defer lib.Close()
			offlineBody:=json.RawMessage(`{"offline":true}`)
			onlineBody:=json.RawMessage(`{"offline":false}`)
			checkRefused:=func(){
				t.Helper()
				e,err:=lib.Call(ctx,workerproto.TransferProbeStart,nil)
				if err!=nil||e.Error==nil||e.Error.Code!="offline"{t.Fatalf("admission: %+v %v",e,err)}
			}
			checkRefused()
			e,err:=lib.Call(ctx,workerproto.SetOffline,onlineBody)
			if err!=nil||e.Error!=nil{t.Fatalf("grant: %+v %v",e,err)}
			e,err=lib.Call(ctx,workerproto.TransferProbeStart,nil)
			if err!=nil||e.Error!=nil||!child.armed.Load(){t.Fatalf("start: %+v %v",e,err)}
			type reply struct{e workerproto.Envelope;err error}
			replyCh:=make(chan reply,1)
			go func(){e,err:=lib.Call(ctx,workerproto.SetOffline,offlineBody);replyCh<-reply{e,err}}()
			select{case <-entered:case <-ctx.Done():t.Fatal(ctx.Err())}
			checkRefused()
			select{case r:=<-replyCh:t.Fatalf("ack before proof: %+v",r);default:}
			close(release)
			select{
			case r:=<-replyCh:
				if fail{
					if r.err==nil&&r.e.Error==nil{t.Fatal("unconfirmed cleanup acknowledged")}
				}else{
					if r.err!=nil||r.e.Error!=nil||!child.stopped.Load(){t.Fatalf("drain: %+v",r)}
					var ack workerproto.OfflineAck
					if err:=json.Unmarshal(r.e.Payload,&ack);err!=nil||!ack.Offline||!ack.Drained{t.Fatalf("ack: %+v %v",ack,err)}
					checkRefused()
				}
			case <-ctx.Done():t.Fatal(ctx.Err())
			}
			lib.Close()
			select{case err:=<-workerDone:if err!=nil{t.Fatal(err)};case <-ctx.Done():t.Fatal(ctx.Err())}
			b.Close()
			err=waitProbeCleanup(t,ctx,done)
			if fail{
				if !errors.Is(err,ErrOperationUnconfirmed){t.Fatal(err)}
				child.fail.Store(false)
				if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
			}else if err!=nil{t.Fatal(err)}
		})
	}
}

func TestTransferProbePersistedRefusalLeavesWorkerUsable(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=NewOperationSet(2)
	b,c,done:=openTransferProbeFixture(t,ctx,set,func(context.Context)(OperationChild,error){return new(operationFixture),nil},nil,func(context.Context)(OperationChild,error){
		// Simulates the library persisting offline after the worker's
		// earlier grant, but before this admitted start reaches custody.
		return nil,workerproto.ErrOffline
	})
	// Reject command-shaped payloads before calling the starter.
	e,err:=b.Call(ctx,workerproto.OperationTransferProbeStart,json.RawMessage(`{"url":"https://example.invalid"}`))
	if err!=nil||e.Error==nil||e.Error.Code!="invalid"{t.Fatalf("invalid: %+v %v",e,err)}
	lr,lw,err:=os.Pipe();if err!=nil{t.Fatal(err)}
	wr,ww,err:=os.Pipe();if err!=nil{_ = lr.Close();_ = lw.Close();t.Fatal(err)}
	defer lr.Close();defer lw.Close();defer wr.Close();defer ww.Close()
	workerDone:=make(chan error,1)
	go func(){workerDone<-workerproto.ServeLifecycleProbeWithBroker(ctx,wr,lw,c)}()
	lib,err:=workerproto.OpenSession(ctx,lr,ww,workerproto.Library,nil)
	if err!=nil{t.Fatal(err)}
	defer lib.Close()
	e,err=lib.Call(ctx,workerproto.SetOffline,json.RawMessage(`{"offline":false}`))
	if err!=nil||e.Error!=nil{t.Fatalf("grant: %+v %v",e,err)}
	e,err=lib.Call(ctx,workerproto.TransferProbeStart,nil)
	if err!=nil||e.Error==nil||e.Error.Code!="offline"{t.Fatalf("persisted refusal: %+v %v",e,err)}
	e,err=lib.Call(ctx,workerproto.SetOffline,json.RawMessage(`{"offline":true}`))
	if err!=nil||e.Error!=nil{t.Fatalf("drain: %+v %v",e,err)}
	ack,err:=workerproto.DecodeOfflineAck(e.Payload)
	if err!=nil||!ack.Offline||!ack.Drained{t.Fatalf("ack: %+v %v",ack,err)}
	select{case <-b.Done():t.Fatal("explicit no-start refusal closed broker");default:}
	lib.Close()
	select{case err:=<-workerDone:if err!=nil{t.Fatal(err)};case <-ctx.Done():t.Fatal(ctx.Err())}
	b.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
}
