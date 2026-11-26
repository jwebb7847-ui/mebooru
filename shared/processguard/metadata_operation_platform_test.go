//go:build darwin || linux || windows

package processguard

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Actual guarded grandchild liveness/EOF, synthetic metadata and result.
// The fixture is held alive until Stop, not a real gallery-dl invocation.
// This exercises the new adapter with independently owned OS processes.
type containedMetadataFixture struct{
	OperationChild
	reader *bufio.Reader
	readMu sync.Mutex
	pipe *os.File
	payload string
}
func(c *containedMetadataFixture)ReadChunk(ctx context.Context)([]byte,bool,error){
	type reply struct{line string;err error}
	done:=make(chan reply,1)
	go func(){c.readMu.Lock();defer c.readMu.Unlock();line,err:=c.reader.ReadString('\n');done<-reply{line,err}}()
	select{
	case r:=<-done:
		if r.err!=nil||r.line!="ready\n"{return nil,false,ErrMetadataOperation}
		return []byte(c.payload),true,nil
	case <-ctx.Done():return nil,false,ctx.Err()
	}
}
func(c *containedMetadataFixture)Result(context.Context)(int,[]byte,error){return 0,nil,nil}
func(c *containedMetadataFixture)Stop(ctx context.Context)error{
	if err:=c.OperationChild.Stop(ctx);err!=nil{return err}
	done:=make(chan error,1)
	go func(){c.readMu.Lock();defer c.readMu.Unlock();_,err:=c.reader.ReadByte();done<-err}()
	defer c.pipe.Close()
	select{
	case err:=<-done:if !errors.Is(err,io.EOF){return ErrOperationUnconfirmed};return nil
	case <-ctx.Done():return ctx.Err()
	}
}
func TestMetadataOperationContainedProcessFixture(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),35*time.Second)
	defer cancel()
	q,r,c,id:=metadataGrant(t)
	set:=NewOperationSet(1)
	defer func(){
		cleanup,done:=context.WithTimeout(context.Background(),10*time.Second)
		defer done()
		if err:=set.SealAndStop(cleanup);err!=nil{t.Errorf("cleanup: %v",err)}
	}()
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	unrelated:=unrelatedWitness(t,ctx)
	starts:=0
	o,err:=NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},
		func(_ context.Context,kind workerproto.MetadataKind,_ []string)(StreamProbeChild,error){
			starts++
			reader,writer,err:=os.Pipe()
			if err!=nil{return nil,err}
			child,err:=startContainedOperationFixture(t,writer)
			_ = writer.Close()
			if child==nil{_ = reader.Close();return nil,err}
			payload:=fixtureMetadata
			if kind==workerproto.MetadataClassify{payload=fixtureClassification}
			return &containedMetadataFixture{OperationChild:child,reader:bufio.NewReader(reader),pipe:reader,payload:payload},err
		})
	if err!=nil{t.Fatal(err)}
	out,err:=o.Run(ctx,q)
	if err!=nil||string(out)!=fixtureMetadata||starts!=2{t.Fatalf("contained metadata: %q %v, starts %d",out,err,starts)}
	if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
	unrelated()
}
