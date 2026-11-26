package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// The leaf owns a fixed local catalog command. Stdout is the lossless
// stream; stderr is reserved for a bounded trusted wrapper result.
// Neither stream contains a custody proof. No URL or argv is accepted.
func runRuntimeStreamLeaf(digest string)error{
	root,err:=runtimeProbeRoot()
	if err!=nil{return err}
	path,err:=verifyRuntimeProbeBinary(root,digest)
	if err!=nil{return err}
	cmd:=exec.Command(path,"--config-ignore","--list-extractors")
	cmd.Dir=root
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

type runtimeStreamOperation struct{
	processguard.OperationChild
	stdout,status *os.File
	result chan runtimeStreamResult
}
type runtimeStreamResult struct{body []byte;err error}
func(o *runtimeStreamOperation)Activate()error{
	if err:=o.OperationChild.Activate();err!=nil{return err}
	// Drain the separate result pipe concurrently, so a full result
	// pipe cannot deadlock stdout EOF. Memory is explicitly bounded.
	go func(){
		b,err:=io.ReadAll(io.LimitReader(o.status,runtimeProbeResultLimit+1))
		o.result<-runtimeStreamResult{b,err}
	}()
	return nil
}
func(o *runtimeStreamOperation)ReadChunk(ctx context.Context)([]byte,bool,error){
	type chunk struct{data []byte;err error}
	result:=make(chan chunk,1)
	go func(){
		buf:=make([]byte,workerproto.StreamChunkLimit)
		n,err:=o.stdout.Read(buf)
		result<-chunk{buf[:n],err}
	}()
	select{
	case got:=<-result:
		if got.err==io.EOF{return got.data,true,nil}
		return got.data,false,got.err
	case <-ctx.Done():
		// The broker poisons this cursor and requires Stop. The single
		// pending reader is released when the owned process exits.
		return nil,false,ctx.Err()
	}
}
func(o *runtimeStreamOperation)Result(ctx context.Context)(int,[]byte,error){
	select{
	case got:=<-o.result:
		if got.err!=nil||len(got.body)>runtimeProbeResultLimit{return 0,nil,errRuntimeProbe}
		var r runtimeVersionResult
		if err:=json.Unmarshal(got.body,&r);err!=nil||!r.Started||r.Failed||r.Overflow||len(r.Stdout)!=0||len(r.Stderr)>workerproto.StreamStderrLimit{return 0,nil,errRuntimeProbe}
		return r.ExitCode,r.Stderr,nil
	case <-ctx.Done():return 0,nil,ctx.Err()
	}
}
func(o *runtimeStreamOperation)Stop(ctx context.Context)error{
	err:=o.OperationChild.Stop(ctx)
	_ = o.stdout.Close()
	_ = o.status.Close()
	return err
}
func runtimeStreamStarter(digest string)processguard.StreamProbeStarter{
	return func(ctx context.Context)(processguard.StreamProbeChild,error){
		if err:=ctx.Err();err!=nil{return nil,err}
		r,w,err:=os.Pipe()
		if err!=nil{return nil,err}
		defer w.Close()
		s,t,err:=os.Pipe()
		if err!=nil{_ = r.Close();return nil,err}
		defer t.Close()
		child,err:=startRuntimeStreamPlatform(w,t,digest)
		if child==nil{_ = r.Close();_ = s.Close();return nil,err}
		return &runtimeStreamOperation{OperationChild:child,stdout:r,status:s,result:make(chan runtimeStreamResult,1)},err
	}
}
