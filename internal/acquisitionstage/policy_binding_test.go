package acquisitionstage

import(
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)
func TestBoundPolicyExactSealedAllocationAndRecovery(t *testing.T){
	ctx:=context.Background()
	for _,mode:=range []string{"valid","wrong digest","wrong ID","foreign URL","partial binding","tampered artifact","tampered record","after publication","low space","revoked"}{
		t.Run(mode,func(t *testing.T){
			a,stage,_,g,auth,f:=publicationFixture(t)
			defer func(){_ = a.Close()}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.jpg",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"}}
			if mode=="wrong ID"{row.PostID="41"}
			if mode=="foreign URL"{row.MediaURL="https://example.invalid/99.jpg"}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(g.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if mode=="wrong digest"{hash=strings.Repeat("b",64)}
			calls:=0
			observe:=func(_ context.Context,actual workerproto.StageAllocation)([]byte,string,error){
				calls++
				if actual!=g{return nil,"",ErrStageRefused}
				return body,hash,nil
			}
			path:=filepath.Join(stage,g.Request.OperationID,"sealed","policy")
			if mode=="partial binding"{if err:=os.Mkdir(path,0700);err!=nil{t.Fatal(err)}}
			if mode=="after publication"{if err:=a.preparePublication(ctx,g);err!=nil{t.Fatal(err)}}
			if mode=="low space"{a.freeBytes=func(string)(uint64,error){return 0,nil}}
			err=a.BindPolicy(ctx,g,observe)
			switch mode{
			case "wrong digest","wrong ID","foreign URL","partial binding","after publication","low space":
				if err==nil{t.Fatal("invalid binding accepted")}
				if mode=="partial binding"{
					if _,err:=a.Allocate(ctx,g.Request);err==nil{t.Fatal("partial binding ignored")}
				}else{
					if _,err:=os.Lstat(path);!errors.Is(err,os.ErrNotExist){t.Fatal("invalid binding mutated stage",err)}
				}
				return
			}
			if err!=nil{t.Fatal(err)}
			if calls!=1{t.Fatal("observer count")}
			if err:=a.BindPolicy(ctx,g,observe);err==nil||calls!=1{t.Fatal("binding replay")}
			body[0]='!'
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
			switch mode{
			case "tampered artifact":
				if err:=os.WriteFile(filepath.Join(path,"projection.json"),body,0600);err!=nil{t.Fatal(err)}
			case "tampered record":
				raw,err:=os.ReadFile(filepath.Join(path,"binding.json"));if err!=nil{t.Fatal(err)}
				raw=[]byte(strings.Replace(string(raw),hash,strings.Repeat("c",64),1))
				if err:=os.WriteFile(filepath.Join(path,"binding.json"),raw,0600);err!=nil{t.Fatal(err)}
			case "revoked":a.authority=func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return Scope{},ErrStageRefused}
			}
			got,err:=a.BoundPolicy(ctx,g)
			if mode=="valid"{
				if err!=nil||len(got.Rows)!=1||got.Rows[0].PostID!=f.ID||got.Rows[0].Rating!="general"{t.Fatalf("bound %+v %v",got,err)}
				if err:=a.preparePublication(ctx,g);err!=nil{t.Fatal(err)}
				if _,err:=a.publishPrepared(ctx,g);err!=nil{t.Fatal(err)}
				if _,err:=a.BoundPolicy(ctx,g);err!=nil{t.Fatal("policy lost after publication",err)}
			}else if err==nil||got.Version!=0{t.Fatal("changed policy accepted")}
		})
	}
}
