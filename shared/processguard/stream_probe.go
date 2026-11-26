package processguard

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Trusted dormant fixed-output fixture only. ReadChunk must honor
// cancellation and never return more than StreamChunkLimit bytes.
// Stop must interrupt pending I/O and prove containment. Result is
// independent of EOF; neither is proof of cleanup.
type StreamProbeChild interface{
	OperationChild
	ReadChunk(context.Context)([]byte,bool,error)
	Result(context.Context)(int,[]byte,error)
}
type StreamProbeStarter func(context.Context)(StreamProbeChild,error)
type streamProbe struct{
	mu sync.Mutex
	lease *OperationLease
	child StreamProbeChild
	offset uint64
	reading,eof,failed,closing bool
}
func streamFault(code string)(json.RawMessage,*workerproto.Fault){return nil,&workerproto.Fault{Code:code}}
func(b *probeBroker)streamStop(ctx context.Context,id uint64,s *streamProbe)error{
	cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
	defer done()
	if err:=s.lease.Stop(cleanup);err!=nil{
		b.mu.Lock();b.sealed=true;b.revoke();b.mu.Unlock()
		return err
	}
	b.mu.Lock();delete(b.streams,id);b.mu.Unlock()
	return nil
}
func(b *probeBroker)handleStream(ctx context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
	if b.stream==nil{return streamFault("unsupported")}
	if op==workerproto.OperationStreamStart{
		if len(body)!=0{return streamFault("invalid")}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.sealed||b.next==^uint64(0){return streamFault("closed")}
		var child StreamProbeChild
		lease,err:=b.set.Start(ctx,func(start context.Context)(OperationChild,error){
			var err error
			child,err=b.stream(start)
			if child==nil{return nil,err}
			return child,err
		})
		if err!=nil||ctx.Err()!=nil{
			if lease!=nil{
				cleanup,done:=context.WithTimeout(context.WithoutCancel(ctx),probeCleanupTimeout)
				stopErr:=lease.Stop(cleanup);done()
				if stopErr!=nil{b.sealed=true;b.revoke();return streamFault("cleanup_unconfirmed")}
			}
			if errors.Is(err,ErrOperationUnconfirmed){b.sealed=true;b.revoke();return streamFault("cleanup_unconfirmed")}
			if errors.Is(err,ErrOperationLimit){return streamFault("busy")}
			return streamFault("start_failed")
		}
		b.next++
		b.streams[b.next]=&streamProbe{lease:lease,child:child}
		out,_:=json.Marshal(workerproto.ProbeOperationRef{ID:b.next})
		return out,nil
	}
	var q workerproto.StreamCursor
	if op==workerproto.OperationStreamStop{
		ref,err:=workerproto.DecodeProbeOperationRef(body)
		if err!=nil{return streamFault("invalid")}
		q.ID=ref.ID
	}else{
		var err error
		q,err=workerproto.DecodeStreamCursor(body)
		if err!=nil{return streamFault("invalid")}
	}
	b.mu.Lock()
	s:=b.streams[q.ID]
	b.mu.Unlock()
	if s==nil{return streamFault("unknown_operation")}
	s.mu.Lock()
	if op==workerproto.OperationStreamStop{
		// Mark unusable before releasing the mutex. No subsequent read
		// can be admitted; a concurrent reader cannot publish success.
		s.failed,s.closing=true,true
		s.mu.Unlock()
		if err:=b.streamStop(ctx,q.ID,s);err!=nil{return streamFault("cleanup_unconfirmed")}
		out,_:=json.Marshal(workerproto.ProbeOperationRef{ID:q.ID})
		return out,nil
	}
	if s.closing||s.failed||s.reading||s.offset!=q.Offset{
		s.mu.Unlock()
		return streamFault("invalid_cursor")
	}
	switch op{
	case workerproto.OperationStreamRead:
		if s.eof{s.mu.Unlock();return streamFault("invalid_cursor")}
		s.reading=true
		s.mu.Unlock()
		read,done:=context.WithTimeout(ctx,15*time.Second)
		data,eof,err:=s.child.ReadChunk(read)
		readErr:=read.Err()
		done()
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reading=false
		if s.closing||s.failed||err!=nil||readErr!=nil||len(data)>workerproto.StreamChunkLimit||
			uint64(len(data))>workerproto.StreamOutputLimit-s.offset||(len(data)==0&&!eof){
			s.failed=true
			return streamFault("stream_failed")
		}
		if data==nil{data=[]byte{}}
		out,_:=json.Marshal(workerproto.StreamChunk{StreamCursor:q,Data:data,EOF:eof})
		s.offset+=uint64(len(data))
		s.eof=eof
		return out,nil
	case workerproto.OperationStreamFinish:
		if !s.eof{s.mu.Unlock();return streamFault("output_not_drained")}
		s.closing=true
		s.mu.Unlock()
		wait,done:=context.WithTimeout(ctx,15*time.Second)
		exit,stderr,err:=s.child.Result(wait)
		waitErr:=wait.Err()
		done()
		if stopErr:=b.streamStop(ctx,q.ID,s);stopErr!=nil{return streamFault("cleanup_unconfirmed")}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.failed||err!=nil||waitErr!=nil||ctx.Err()!=nil{return streamFault("stream_failed")}
		if len(stderr)>workerproto.StreamStderrLimit{return streamFault("stream_failed")}
		if stderr==nil{stderr=[]byte{}}
		out,_:=json.Marshal(workerproto.StreamTerminal{StreamCursor:q,ExitCode:exit,Stderr:stderr})
		if _,err:=workerproto.DecodeStreamTerminal(out);err!=nil{return streamFault("stream_failed")}
		return out,nil
	default:
		s.mu.Unlock()
		return streamFault("unsupported")
	}
}
