package processguard

import(
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func transferOperationFixture(t *testing.T)(workerproto.TransferPlanRequest,acquisitionplan.MetadataConfig,[]byte,string){
	t.Helper()
	q,_,c,_:=metadataGrant(t)
	raw:=[]byte(`{"1":{"category":"derpibooru","subcategory":"post","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/1.jpg"}}`)
	sum:=sha256.Sum256(raw)
	root,err:=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	return workerproto.TransferPlanRequest{Metadata:q,Subcategory:"post",PolicyGeneration:1,ManifestSHA256:hex.EncodeToString(sum[:])},c,raw,root
}

func TestStrictTransferOperationRequiresEveryProbe(t *testing.T){
	for _,mode:=range []string{"success","wrong version","negative allowed","positive empty","warning","unconfirmed","revocation"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			q,c,manifest,root:=transferOperationFixture(t)
			set:=NewOperationSet(1)
			gate:=workerproto.NewDrainGate(ctx)
			if err:=gate.Open();err!=nil{t.Fatal(err)}
			epoch:=gate.CurrentGeneration()
			allowed:=true
			var stages []StrictTransferStage
			var previous *metadataChildFixture
			out,err:=RunStrictTransfer(ctx,set,q,c,root,func(c context.Context,_ workerproto.TransferPlanRequest)(context.Context,[]byte,func(error),error){
				lease,finish,err:=gate.AdmitBound(c,epoch)
				return lease,manifest,finish,err
			},func(context.Context)bool{return allowed},func(_ context.Context,stage StrictTransferStage,_ []string)(StreamProbeChild,error){
				if previous!=nil&&!previous.stopped.Load(){t.Fatal("prior stage not cleaned")}
				stages=append(stages,stage)
				f:=&metadataChildFixture{}
				switch stage{
				case TransferVersion:
					f.data=acquisitionplan.StrictQualifiedVersion
					if mode=="wrong version"{f.data="1.32.21"}
				case TransferNegative:
					f.data=`[[2,{}]]`
					if mode=="negative allowed"{f.data=`[[2,{}],[3,"x",{}]]`}
					if mode=="warning"{f.stderr=[]byte("secret warning")}
					if mode=="unconfirmed"{f.fail.Store(true)}
					if mode=="revocation"{f.stop=func(c context.Context)error{allowed=false;return f.operationFixture.Stop(c)}}
				case TransferPositive:
					f.data=`[[2,{}],[3,"x",{}]]`
					if mode=="positive empty"{f.data=`[[2,{}]]`}
				case TransferMedia:f.data=""
				}
				previous=f
				return f,nil
			})
			if mode=="success"{
				if err!=nil||len(out)!=0||!slices.Equal(stages,[]StrictTransferStage{TransferVersion,TransferNegative,TransferPositive,TransferMedia}){t.Fatalf("success: %v %v %v",out,err,stages)}
			}else{
				if err==nil||out!=nil||slices.Contains(stages,TransferMedia){t.Fatalf("failed probe launched transfer: %v %v",err,stages)}
			}
			if mode=="unconfirmed"{
				if !errors.Is(err,ErrOperationUnconfirmed)||!errors.Is(gate.Drain(ctx),workerproto.ErrDrainUnconfirmed)||gate.Open()==nil{t.Fatal("lost cleanup uncertainty")}
				previous.fail.Store(false)
			}else if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
			if err:=set.SealAndStop(ctx);err!=nil{t.Fatal(err)}
		})
	}
}
