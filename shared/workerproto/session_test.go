package workerproto

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func pairSessions(t *testing.T, libHandler, workerHandler Handler) (*Session, *Session, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	type result struct { s *Session; err error }
	ready := make(chan result, 1)
	go func() { s, err := OpenSession(ctx,b,b,Worker,workerHandler); ready <- result{s,err} }()
	lib, err := OpenSession(ctx,a,a,Library,libHandler)
	if err != nil { t.Fatal(err) }
	t.Cleanup(lib.Close)
	worker := <-ready
	if worker.err != nil { t.Fatal(worker.err) }
	t.Cleanup(worker.s.Close)
	return lib,worker.s,ctx
}

func TestSessionConcurrentCallsAndCallback(t *testing.T) {
	workerReady := make(chan *Session, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	lib,w,ctx := pairSessions(t,
		func(_ context.Context, op Operation, _ json.RawMessage)(json.RawMessage,*Fault){
			if op != GalleryList { return nil,&Fault{Code:"unexpected"} }
			return json.RawMessage("{\"gallery\":\"fixture\"}"),nil
		},
		func(ctx context.Context, op Operation, _ json.RawMessage)(json.RawMessage,*Fault){
			if op == Health {
				close(started)
				select { case <-release: case <-ctx.Done(): return nil,&Fault{Code:"canceled"} }
				return json.RawMessage("{\"health\":true}"),nil
			}
			worker := <-workerReady
			e,err := worker.Call(ctx,GalleryList,nil)
			if err != nil || e.Error != nil { return nil,&Fault{Code:"callback_failed"} }
			return e.Payload,nil
		})
	workerReady <- w
	type result struct { e Envelope; err error }
	slow := make(chan result,1)
	go func(){ e,err := lib.Call(ctx,Health,nil); slow<-result{e,err} }()
	select { case <-started: case <-ctx.Done(): t.Fatal(ctx.Err()) }
	e,err := lib.Call(ctx,QueueList,nil)
	if err != nil || e.Error != nil || string(e.Payload)!="{\"gallery\":\"fixture\"}" { t.Fatalf("callback: %+v %v",e,err) }
	close(release)
	r := <-slow
	if r.err != nil || r.e.Error != nil || string(r.e.Payload)!="{\"health\":true}" || r.e.ID == e.ID { t.Fatalf("slow: %+v",r) }
	if _,err := w.Call(ctx,Health,nil); !errors.Is(err,ErrDirection){ t.Fatalf("wrong direction: %v",err) }
}

func TestSessionCancellationAndReuse(t *testing.T) {
	started,observed := make(chan struct{}),make(chan struct{})
	lib,_,ctx := pairSessions(t,nil,func(ctx context.Context,op Operation,_ json.RawMessage)(json.RawMessage,*Fault){
		if op == QueueGet {
			close(started)
			<-ctx.Done()
			close(observed)
			return nil,&Fault{Code:"canceled"}
		}
		return nil,nil
	})
	callCtx,cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error,1)
	go func(){ _,err:=lib.Call(callCtx,QueueGet,nil); result<-err }()
	select {case <-started:case <-ctx.Done():t.Fatal(ctx.Err())}
	cancel()
	if err:=<-result; !errors.Is(err,context.Canceled){t.Fatalf("cancel: %v",err)}
	select {case <-observed:case <-ctx.Done():t.Fatal(ctx.Err())}
	if e,err:=lib.Call(ctx,Health,nil);err!=nil || e.Error!=nil{t.Fatalf("reuse: %+v %v",e,err)}
}

func TestSessionBoundedAdmissionAndPeerLoss(t *testing.T) {
	started := make(chan struct{},MaxPendingCalls)
	lib,worker,ctx := pairSessions(t,nil,func(ctx context.Context,_ Operation,_ json.RawMessage)(json.RawMessage,*Fault){
		started<-struct{}{}
		<-ctx.Done()
		return nil,&Fault{Code:"canceled"}
	})
	var wg sync.WaitGroup
	errs:=make(chan error,MaxPendingCalls)
	for i:=0;i<MaxPendingCalls;i++{
		wg.Add(1)
		go func(){defer wg.Done();_,err:=lib.Call(ctx,Health,nil);errs<-err}()
	}
	for i:=0;i<MaxPendingCalls;i++{select{case <-started:case <-ctx.Done():t.Fatal(ctx.Err())}}
	if _,err:=lib.Call(ctx,Health,nil);!errors.Is(err,ErrBusy){t.Fatalf("admission: %v",err)}
	worker.Close()
	wg.Wait()
	close(errs)
	for err:=range errs{if err==nil{t.Fatal("pending call succeeded after peer loss")}}
	select{case <-lib.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
}

func TestSessionProgressDoesNotBlock(t *testing.T) {
	lib,worker,ctx:=pairSessions(t,nil,func(context.Context,Operation,json.RawMessage)(json.RawMessage,*Fault){return nil,nil})
	for i:=0;i<EventBuffer*2;i++{
		if err:=worker.EmitProgress(json.RawMessage("{}"));err!=nil && !errors.Is(err,ErrBackpressure){t.Fatal(err)}
	}
	if e,err:=lib.Call(ctx,Health,nil);err!=nil||e.Error!=nil{t.Fatalf("progress blocked response: %+v %v",e,err)}
	if n:=len(lib.Events());n==0 || n>EventBuffer{t.Fatalf("event bound: %d",n)}
	if err:=lib.EmitProgress(nil);!errors.Is(err,ErrDirection){t.Fatalf("wrong event direction: %v",err)}
}

func TestSessionRefusesUnsolicitedResponse(t *testing.T) {
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
	defer cancel()
	a,b:=net.Pipe()
	defer a.Close()
	defer b.Close()
	ready:=make(chan error,1)
	go func(){ready<-Handshake(b,b,Worker)}()
	lib,err:=OpenSession(ctx,a,a,Library,nil)
	if err!=nil{t.Fatal(err)}
	defer lib.Close()
	if err:=<-ready;err!=nil{t.Fatal(err)}
	if err:=WriteEnvelope(b,Envelope{Kind:Response,ID:99});err!=nil{t.Fatal(err)}
	select{case <-lib.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
	if !errors.Is(lib.Err(),ErrCorrelation){t.Fatalf("unsolicited response: %v",lib.Err())}
}
