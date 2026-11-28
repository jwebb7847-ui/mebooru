package acquisitionstage

import(
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestAbandonPrepublication(t *testing.T){
	for _,mode:=range []string{"empty","nonempty","denied","sealed","prepared","lost acknowledgement"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var a *Allocator
			var stage string
			var grant workerproto.StageAllocation
			var authority Authority
			if mode=="sealed"||mode=="prepared"{
				var s Scope
				a,stage,s,grant,authority,_=publicationFixture(t)
				_ = s
				if mode=="prepared"{if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}}
			}else{
				st,s,q:=fixture(t);stage=st
				authority=func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return s,nil}
				var err error
				a,err=Open(stage,1,2048,authority);if err!=nil{t.Fatal(err)}
				grant,err=a.Allocate(ctx,q);if err!=nil{t.Fatal(err)}
			}
			defer func(){a.Close()}()
			w:=filepath.Join(stage,grant.Request.OperationID,grant.TransferID)
			if mode=="nonempty"{if err:=os.WriteFile(filepath.Join(w,"partial"),[]byte("retain"),0600);err!=nil{t.Fatal(err)}}
			q:=workerproto.StageAbandonRequest{OperationID:grant.Request.OperationID,TransferID:grant.TransferID}
			calls:=0
			auth:=AbandonAuthority(func(context.Context,workerproto.StageAllocation)error{
				calls++
				if mode=="denied"||mode=="lost acknowledgement"&&calls==3{return ErrStageRefused}
				return nil
			})
			m,err:=a.AbandonPrepublication(ctx,q,auth)
			if mode=="denied"||mode=="prepared"{
				if err==nil{t.Fatal("abandoned without safe authority")}
				if _,err:=os.Stat(filepath.Join(stage,q.OperationID,"abandoned"));!os.IsNotExist(err){t.Fatal("wrote abandonment")}
				return
			}
			if mode=="lost acknowledgement"{
				if err==nil{t.Fatal("acknowledged revoked operation")}
				m,err=a.ObserveAbandoned(ctx,q)
			}
			want:=uint64(0);if mode=="nonempty"||mode=="sealed"{want=grant.MaxBytes}
			if err!=nil||m.RetainedReservation!=want{t.Fatal("abandoned",m,err)}
			if _,err:=a.Workspace(ctx,grant);err==nil{t.Fatal("workspace reopened")}
			if _,err:=a.Allocate(ctx,grant.Request);!errors.Is(err,ErrStageRefused){t.Fatal("allocation replay",err)}
			if _,err:=a.Sealed(ctx,grant);err==nil{t.Fatal("abandoned seal reopened")}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,1,4096,authority);if err!=nil{t.Fatal(err)}
			again,err:=a.ObserveAbandoned(ctx,q);if err!=nil||again!=m{t.Fatal("restart",again,err)}
			_,closed,reserved,err:=a.inventoryWithClosure(ctx)
			if err!=nil||!closed[q.OperationID]||reserved!=want{t.Fatal("accounting",closed,reserved,err)}
			next:=grant.Request;next.OperationID=strings.Repeat("d",32);next.Transfer.Metadata.JobID++
			if _,err:=a.Allocate(ctx,next);err!=nil{t.Fatal("active slot not released",err)}
			if mode=="nonempty"{b,err:=os.ReadFile(filepath.Join(w,"partial"));if err!=nil||string(b)!="retain"{t.Fatal("lost evidence",err)}}
		})
	}
}
