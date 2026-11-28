package processguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

type streamFixture struct{
	operationFixture
	data []byte
	read func(context.Context)([]byte,bool,error)
	result func(context.Context)(int,[]byte,error)
	beforeStop func(context.Context)error
}
func(f *streamFixture)ReadChunk(ctx context.Context)([]byte,bool,error){
	if f.read!=nil{return f.read(ctx)}
	n:=min(len(f.data),workerproto.StreamChunkLimit)
	data:=append([]byte{},f.data[:n]...)
	f.data=f.data[n:]
	return data,len(f.data)==0,nil
}
func(f *streamFixture)Result(ctx context.Context)(int,[]byte,error){
	if f.result!=nil{return f.result(ctx)}
	return 7,[]byte("fixture stderr"),nil
}
func(f *streamFixture)Stop(ctx context.Context)error{
	if f.beforeStop!=nil{if err:=f.beforeStop(ctx);err!=nil{return err}}
	return f.operationFixture.Stop(ctx)
}
func openStreamFixture(t *testing.T,ctx context.Context,set *OperationSet,start ProbeStarter,stream StreamProbeStarter)(*workerproto.Session,*workerproto.StreamProbeClient,*workerproto.ProbeClient,<-chan error){
	t.Helper()
	cr,cw,err:=os.Pipe();if err!=nil{t.Fatal(err)}
	sr,sw,err:=os.Pipe();if err!=nil{_ = cr.Close();_ = cw.Close();t.Fatal(err)}
	t.Cleanup(func(){_ = cr.Close();_ = cw.Close();_ = sr.Close();_ = sw.Close()})
	done:=make(chan error,1)
	go func(){done<-ServeProbeBrokerWithStream(ctx,sr,cw,set,start,nil,stream)}()
	s,err:=workerproto.OpenSession(ctx,cr,sw,workerproto.OperationRequester,nil)
	if err!=nil{t.Fatal(err)}
	t.Cleanup(s.Close)
	c,err:=workerproto.NewStreamProbeClient(s);if err!=nil{t.Fatal(err)}
	p,err:=workerproto.NewProbeClient(s);if err!=nil{t.Fatal(err)}
	return s,c,p,done
}
func TestStreamProbeLosslessAndTerminalAfterCleanup(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
	sibling:=new(operationFixture)
	// Includes arbitrary binary bytes, newline boundaries and several chunks.
	want:=bytes.Repeat([]byte{0,255,10,13,65,128,42},20000)
	entered,release:=make(chan struct{}),make(chan struct{})
	defer func(){select{case <-release:default:close(release)}}()
	f:=&streamFixture{data:append([]byte{},want...),beforeStop:func(ctx context.Context)error{
		close(entered)
		select{case <-release:return nil;case <-ctx.Done():return ctx.Err()}
	}}
	s,c,p,done:=openStreamFixture(t,ctx,NewOperationSet(2),startOperationFixture(sibling),func(context.Context)(StreamProbeChild,error){return f,nil})
	other,err:=p.Start(ctx);if err!=nil{t.Fatal(err)}
	ref,err:=c.Start(ctx);if err!=nil{t.Fatal(err)}
	q:=workerproto.StreamCursor{ID:ref.ID}
	body,_:=json.Marshal(q)
	e,err:=s.Call(ctx,workerproto.OperationStreamFinish,body)
	if err!=nil||e.Error==nil||e.Error.Code!="output_not_drained"{t.Fatalf("early finish: %+v %v",e,err)}
	var out []byte
	for{
		chunk,err:=c.Read(ctx,q);if err!=nil{t.Fatal(err)}
		out=append(out,chunk.Data...)
		q.Offset+=uint64(len(chunk.Data))
		if chunk.EOF{break}
		// Replaying a consumed offset must not advance the producer.
		if _,err:=c.Read(ctx,workerproto.StreamCursor{ID:ref.ID});err==nil{t.Fatal("stale cursor accepted")}
	}
	if !bytes.Equal(out,want){t.Fatal("stream lost or reordered bytes")}
	type answer struct{result workerproto.StreamTerminal;err error}
	finished:=make(chan answer,1)
	go func(){v,err:=c.Finish(ctx,q);finished<-answer{v,err}}()
	select{case <-entered:case <-ctx.Done():t.Fatal(ctx.Err())}
	select{case got:=<-finished:t.Fatalf("terminal before proof: %+v",got);default:}
	close(release)
	select{
	case got:=<-finished:
		if got.err!=nil||got.result.ExitCode!=7||string(got.result.Stderr)!="fixture stderr"||!f.stopped.Load(){t.Fatalf("terminal: %+v",got)}
	case <-ctx.Done():t.Fatal(ctx.Err())
	}
	if sibling.stopped.Load(){t.Fatal("sibling stopped")}
	if err:=p.Stop(ctx,other);err!=nil{t.Fatal(err)}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
}
func TestStreamProbeCancelReadAndStopPreserveSibling(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
	sibling:=new(operationFixture)
	entered:=make(chan struct{})
	f:=&streamFixture{read:func(ctx context.Context)([]byte,bool,error){close(entered);<-ctx.Done();return nil,false,ctx.Err()}}
	s,c,p,done:=openStreamFixture(t,ctx,NewOperationSet(2),startOperationFixture(sibling),func(context.Context)(StreamProbeChild,error){return f,nil})
	other,err:=p.Start(ctx);if err!=nil{t.Fatal(err)}
	ref,err:=c.Start(ctx);if err!=nil{t.Fatal(err)}
	call,stop:=context.WithCancel(ctx);defer stop()
	result:=make(chan error,1)
	go func(){_,err:=c.Read(call,workerproto.StreamCursor{ID:ref.ID});result<-err}()
	select{case <-entered:case <-ctx.Done():t.Fatal(ctx.Err())}
	stop()
	if err:=<-result;!errors.Is(err,context.Canceled){t.Fatal(err)}
	if err:=c.Stop(ctx,ref);err!=nil{t.Fatal(err)}
	if !f.stopped.Load()||sibling.stopped.Load(){t.Fatal("individual cancellation failed")}
	select{case <-s.Done():t.Fatal("ordinary cancellation closed session");default:}
	if err:=p.Stop(ctx,other);err!=nil{t.Fatal(err)}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
}
func TestStreamProbeFailedCleanupNeverReturnsTerminal(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
	f:=&streamFixture{}
	f.fail.Store(true)
	sibling:=new(operationFixture)
	s,c,p,done:=openStreamFixture(t,ctx,NewOperationSet(2),startOperationFixture(sibling),func(context.Context)(StreamProbeChild,error){return f,nil})
	if _,err:=p.Start(ctx);err!=nil{t.Fatal(err)}
	ref,err:=c.Start(ctx);if err!=nil{t.Fatal(err)}
	q:=workerproto.StreamCursor{ID:ref.ID}
	chunk,err:=c.Read(ctx,q);if err!=nil||!chunk.EOF{t.Fatalf("EOF: %+v %v",chunk,err)}
	if _,err:=c.Finish(ctx,q);err==nil{t.Fatal("uncertain cleanup returned result")}
	select{case <-s.Done():case <-ctx.Done():t.Fatal(ctx.Err())}
	if err:=waitProbeCleanup(t,ctx,done);!errors.Is(err,ErrOperationUnconfirmed){t.Fatal(err)}
	if !sibling.stopped.Load(){t.Fatal("uncertain stream did not revoke sibling")}
}
func TestStreamProbeOversizedOutputRefused(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
	f:=&streamFixture{read:func(context.Context)([]byte,bool,error){return make([]byte,workerproto.StreamChunkLimit+1),false,nil}}
	s,c,_,done:=openStreamFixture(t,ctx,NewOperationSet(1),startOperationFixture(new(operationFixture)),func(context.Context)(StreamProbeChild,error){return f,nil})
	ref,err:=c.Start(ctx);if err!=nil{t.Fatal(err)}
	if _,err:=c.Read(ctx,workerproto.StreamCursor{ID:ref.ID});err==nil{t.Fatal("oversized output accepted")}
	if err:=c.Stop(ctx,ref);err!=nil{t.Fatal(err)}
	s.Close()
	if err:=waitProbeCleanup(t,ctx,done);err!=nil{t.Fatal(err)}
}
