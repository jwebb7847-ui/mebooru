package workerproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// rawPeer allows negative tests to bypass the normal sending API. No
// real sockets are opened: net.Pipe is an in-memory full-duplex stream.
func rawPeer(t *testing.T, handler Handler) (*Session, net.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	a, b := net.Pipe()
	t.Cleanup(func(){ _ = a.Close(); _ = b.Close() })
	ready := make(chan error, 1)
	go func(){ ready <- Handshake(b,b,Worker) }()
	lib, err := OpenSession(ctx,a,a,Library,handler)
	if err != nil { t.Fatal(err) }
	t.Cleanup(lib.Close)
	if err := <-ready; err != nil { t.Fatal(err) }
	return lib,b,ctx
}

func TestSessionRejectsUnauthorizedUnknownAndDuplicateRequests(t *testing.T) {
	invoked := make(chan struct{},1)
	lib,peer,ctx := rawPeer(t,func(context.Context,Operation,json.RawMessage)(json.RawMessage,*Fault){
		invoked<-struct{}{}
		return nil,nil
	})
	for _, tc := range []struct{ op Operation; code string }{
		{Health,"unauthorized"},
		{Operation("library.sql"),"unsupported"},
	} {
		id:=uint64(1)
		if tc.code=="unsupported"{id=2}
		if err:=WriteEnvelope(peer,Envelope{Kind:Request,ID:id,Op:tc.op});err!=nil{t.Fatal(err)}
		e,err:=ReadEnvelope(peer)
		if err!=nil || e.Kind!=Response || e.ID!=id || e.Error==nil || e.Error.Code!=tc.code {
			t.Fatalf("refusal: %+v %v",e,err)
		}
	}
	// Reuse a completed ID with an otherwise permitted operation.
	if err:=WriteEnvelope(peer,Envelope{Kind:Request,ID:2,Op:GalleryList});err!=nil{t.Fatal(err)}
	select{case <-lib.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
	if !errors.Is(lib.Err(),ErrCorrelation){t.Fatalf("duplicate request: %v",lib.Err())}
	select{case <-invoked:t.Fatal("refused request reached handler");default:}
}

func TestSessionUnacknowledgedCancellationClosesConnection(t *testing.T) {
	lib,peer,ctx:=rawPeer(t,nil)
	callCtx,cancel:=context.WithCancel(ctx)
	defer cancel()
	result:=make(chan error,1)
	go func(){_,err:=lib.Call(callCtx,Health,nil);result<-err}()
	req,err:=ReadEnvelope(peer)
	if err!=nil||req.Kind!=Request{t.Fatalf("request: %+v %v",req,err)}
	cancel()
	if err:=<-result;!errors.Is(err,context.Canceled){t.Fatalf("caller: %v",err)}
	e,err:=ReadEnvelope(peer)
	if err!=nil||e.Kind!=Cancel||e.ID!=req.ID{t.Fatalf("cancel: %+v %v",e,err)}
	// Peer deliberately withholds a terminal acknowledgement.
	select{case <-lib.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
	if !errors.Is(lib.Err(),ErrCancelUnacknowledged){t.Fatalf("session: %v",lib.Err())}
}

func TestSessionQueueByteBudget(t *testing.T) {
	s:= &Session{done:make(chan struct{}),out:make(chan []byte,MaxQueuedFrames),queuedBytes:MaxQueuedBytes-1}
	if err:=s.enqueueLocked([]byte{1,2});!errors.Is(err,ErrBackpressure){t.Fatalf("budget: %v",err)}
	if len(s.out)!=0||s.queuedBytes!=MaxQueuedBytes-1{t.Fatal("rejected message changed budget")}
	if err:=s.enqueueLocked([]byte{1});err!=nil{t.Fatal(err)}
	if s.queuedBytes!=MaxQueuedBytes{t.Fatal("admitted bytes not accounted")}
}

func TestSessionFixtureChild(t *testing.T) {
	if os.Getenv("MEBOORU_SESSION_FIXTURE")!="1"{return}
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
	defer cancel()
	ready:=make(chan *Session,1)
	s,err:=OpenSession(ctx,os.Stdin,os.Stdout,Worker,func(ctx context.Context,op Operation,_ json.RawMessage)(json.RawMessage,*Fault){
		if op!=QueueList{return nil,&Fault{Code:"unsupported"}}
		worker:=<-ready
		e,err:=worker.Call(ctx,GalleryList,nil)
		if err!=nil||e.Error!=nil{return nil,&Fault{Code:"callback_failed"}}
		return e.Payload,nil
	})
	if err!=nil{os.Exit(2)}
	ready<-s
	<-s.Done()
	if !errors.Is(s.Err(),io.EOF){os.Exit(3)}
	os.Exit(0)
}

func TestSessionOverInheritedSubprocess(t *testing.T) {
	ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second)
	defer cancel()
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	cmd:=exec.CommandContext(ctx,exe,"-test.run=^TestSessionFixtureChild$")
	cmd.Env=append(os.Environ(),"MEBOORU_SESSION_FIXTURE=1")
	toChild,err:=cmd.StdinPipe()
	if err!=nil{t.Fatal(err)}
	fromChild,err:=cmd.StdoutPipe()
	if err!=nil{_ = toChild.Close();t.Fatal(err)}
	if err:=cmd.Start();err!=nil{_ = toChild.Close();_ = fromChild.Close();t.Fatal(err)}
	waited:=false
	defer func(){
		_ = toChild.Close()
		_ = fromChild.Close()
		if !waited{_ = cmd.Process.Kill();_ = cmd.Wait()}
	}()
	lib,err:=OpenSession(ctx,fromChild,toChild,Library,func(_ context.Context,op Operation,_ json.RawMessage)(json.RawMessage,*Fault){
		if op!=GalleryList{return nil,&Fault{Code:"unsupported"}}
		return json.RawMessage("{\"gallery\":\"subprocess-fixture\"}"),nil
	})
	if err!=nil{t.Fatal(err)}
	defer lib.Close()
	e,err:=lib.Call(ctx,QueueList,nil)
	if err!=nil||e.Error!=nil||string(e.Payload)!="{\"gallery\":\"subprocess-fixture\"}"{t.Fatalf("subprocess callback: %+v %v",e,err)}
	lib.Close()
	err=cmd.Wait()
	waited=true
	if err!=nil{t.Fatalf("subprocess did not exit cleanly: %v",err)}
}
