package processguard

import(
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

type testWindowResolver struct{
	claim func(context.Context,workerproto.MetadataPlanRequest)(ManagedRuntimeGrant,error)
	settle func(context.Context,workerproto.MetadataPlanRequest,int,bool)error
}
func(r testWindowResolver)Claim(c context.Context,q workerproto.MetadataPlanRequest)(ManagedRuntimeGrant,error){return r.claim(c,q)}
func(r testWindowResolver)Settle(c context.Context,q workerproto.MetadataPlanRequest,p int,f bool)error{return r.settle(c,q,p,f)}

func TestManagedRuntimeSequentialWindows(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
	q,r,c,id:=metadataGrant(t)
	q2:=q;q2.JobID++
	q3:=q2;q3.JobID++
	gate:=workerproto.NewDrainGate(ctx);if err:=gate.Open();err!=nil{t.Fatal(err)}
	var calls atomic.Int32
	var claimed,settled atomic.Int32
	resolver:=testWindowResolver{
		claim:func(_ context.Context,v workerproto.MetadataPlanRequest)(ManagedRuntimeGrant,error){
			n:=claimed.Load()
			if n==0&&v!=q||n==1&&v!=q2||n==2&&v!=q3||n>=3{return ManagedRuntimeGrant{},ErrOperationsSealed}
			claimed.Add(1)
			starts:=0
			return ManagedRuntimeGrant{
				Metadata:v,Runtime:r,Config:c,Current:func(context.Context)bool{return true},
				Transfer:func(context.Context)(workerproto.StageAllocation,string,error){return workerproto.StageAllocation{},"",ErrStrictTransferOperation},
				Start:func(context.Context,[]string)(StreamProbeChild,error){
					calls.Add(1)
					starts++
					body:=fixtureMetadata
					if starts==1{body=fixtureClassification}
					if v==q2{body="unrecognized source"}
					return &metadataChildFixture{data:body},nil
				},
				Complete:func(context.Context,workerproto.StageAllocation,[]acquisitionplan.VerifiedTransferFile)error{return ErrStrictTransferOperation},
			},nil
		},
		settle:func(_ context.Context,v workerproto.MetadataPlanRequest,phase int,failed bool)error{
			if v==q2 {
				if !failed||phase!=0{return errors.New("failed source did not stop before metadata")}
			}else if failed||phase!=2||v!=q&&v!=q3{return errors.New("owner no-transfer result not verified")}
			settled.Add(1);return nil
		},
	}
	cr,cw,err:=os.Pipe();if err!=nil{t.Fatal(err)}
	sr,sw,err:=os.Pipe();if err!=nil{cr.Close();cw.Close();t.Fatal(err)}
	defer cr.Close();defer cw.Close();defer sr.Close();defer sw.Close()
	done:=make(chan error,1)
	go func(){done<-ServeManagedRuntimeWindows(ctx,sr,cw,NewOperationSet(1),resolver)}()
	session,err:=workerproto.OpenSession(ctx,cr,sw,workerproto.OperationRequester,nil);if err!=nil{t.Fatal(err)}
	defer func(){session.Close();select{case err:=<-done:if err!=nil{t.Error(err)};case <-ctx.Done():t.Error("broker cleanup")}}()
	client,err:=NewManagedRuntimeClient(session);if err!=nil{t.Fatal(err)}
	for _,v:=range []workerproto.MetadataPlanRequest{q,q2,q3}{
		if err:=client.OpenWindow(ctx,v);err!=nil{t.Fatal(err)}
		op,err:=NewMetadataOperation(NewOperationSet(1),gate,v,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},client.MetadataStarter(v))
		if err!=nil{t.Fatal(err)}
		_,err=op.Run(ctx,v)
		if v==q2{
			if !errors.Is(err,ErrMetadataOperation){t.Fatalf("rejected source: %v",err)}
		}else if err!=nil{t.Fatal(err)}
		if err:=client.CloseWindow(ctx,v);err!=nil{t.Fatal(err)}
	}
	if calls.Load()!=5||claimed.Load()!=3||settled.Load()!=3{t.Fatal("window counts",calls.Load(),claimed.Load(),settled.Load())}
	if err:=client.OpenWindow(ctx,q);err==nil{t.Fatal("replayed settled job")}
}
