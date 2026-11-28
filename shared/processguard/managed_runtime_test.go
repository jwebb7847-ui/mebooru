package processguard

import(
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestManagedRuntimeBrokerMetadataSequence(t *testing.T){
	for _,mode:=range []string{"success","resolve first","changed gallery","bad classification"}{
		t.Run(mode,func(t *testing.T){
			ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second);defer cancel()
			q,r,c,id:=metadataGrant(t)
			gate:=workerproto.NewDrainGate(ctx);if err:=gate.Open();err!=nil{t.Fatal(err)}
			var calls atomic.Int32
			var previous *metadataChildFixture
			cr,cw,err:=os.Pipe();if err!=nil{t.Fatal(err)}
			sr,sw,err:=os.Pipe();if err!=nil{cr.Close();cw.Close();t.Fatal(err)}
			defer cr.Close();defer cw.Close();defer sr.Close();defer sw.Close()
			done:=make(chan error,1)
			go func(){done<-ServeManagedRuntimeBroker(ctx,sr,cw,NewOperationSet(1),ManagedRuntimeGrant{
				Metadata:q,Runtime:r,Config:c,Current:func(context.Context)bool{return true},
				Transfer:func(context.Context)(workerproto.StageAllocation,string,error){return workerproto.StageAllocation{},"",ErrStrictTransferOperation},
				Start:func(_ context.Context,args []string)(StreamProbeChild,error){
					n:=calls.Add(1)
					if previous!=nil&&!previous.stopped.Load(){return nil,ErrOperationUnconfirmed}
					f:=&metadataChildFixture{data:fixtureMetadata}
					if n==1{f.data=fixtureClassification;if mode=="bad classification"{f.data="bad"}}
					previous=f
					return f,nil
				},
				Complete:func(context.Context,workerproto.StageAllocation,[]acquisitionplan.VerifiedTransferFile)error{return ErrStrictTransferOperation},
			})}()
			session,err:=workerproto.OpenSession(ctx,cr,sw,workerproto.OperationRequester,nil);if err!=nil{t.Fatal(err)}
			defer func(){session.Close();select{case err:=<-done:if err!=nil{t.Error(err)};case <-ctx.Done():t.Error("broker cleanup")}}()
			if mode=="resolve first"||mode=="changed gallery"{
				bad:=q
				if mode=="changed gallery"{bad.Kind=workerproto.MetadataClassify;bad.First,bad.Last=0,0;bad.Gallery="B"}
				body,_:=json.Marshal(workerproto.ManagedMetadataStart{Request:bad})
				e,err:=session.Call(ctx,workerproto.OperationMetadataStart,body)
				if err!=nil||e.Error==nil||calls.Load()!=0{t.Fatal("unapproved runtime activation",err)}
				return
			}
			client,err:=NewManagedRuntimeClient(session);if err!=nil{t.Fatal(err)}
			op,err:=NewMetadataOperation(NewOperationSet(1),gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},client.MetadataStarter(q))
			if err!=nil{t.Fatal(err)}
			out,err:=op.Run(ctx,q)
			if mode=="success"{
				if err!=nil||string(out)!=fixtureMetadata||calls.Load()!=2{t.Fatal("broker result",string(out),err)}
			}else if err==nil||calls.Load()!=1{t.Fatal("invalid classification progressed",err)}
		})
	}
}
