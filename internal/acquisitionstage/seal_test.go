package acquisitionstage

import(
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestAllocatorSealAndReconcile(t *testing.T){
	for _,mode:=range []string{"valid","empty","unconfirmed","changed source","partial marker","changed sealed bytes","changed sealed manifest","revoked after seal"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			stage,s,q:=fixture(t)
			manifest:=[]byte(`{"42":{"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg"}}`)
			q.Transfer.ManifestSHA256=digest(manifest)
			s.MaxBytes=2048
			allowed:=true
			auth:=func(context.Context,workerproto.StageAllocateRequest)(Scope,error){
				if !allowed{return Scope{},ErrStageRefused};return s,nil
			}
			a,err:=Open(stage,2,4096,auth)
			if err!=nil{t.Fatal(err)}
			defer func(){_ = a.Close()}()
			g,err:=a.Allocate(ctx,q)
			if err!=nil{t.Fatal(err)}
			workspace,err:=a.Workspace(ctx,g)
			if err!=nil{t.Fatal(err)}
			cmd,err:=acquisitionplan.PrepareTransferCommands(q.Transfer,manifest,acquisitionplan.MetadataConfig{Generation:1,Source:q.Transfer.Metadata.Source},workspace)
			if err!=nil{t.Fatal(err)}
			path:=filepath.Join(workspace,"media","42.jpg")
			meta:=[]byte(`{"id":42,"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg"}`)
			data:=[]byte("fixture")
			stdout:=""
			if mode!="empty"{
				if err:=os.WriteFile(path,data,0600);err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(path+".json",meta,0600);err!=nil{t.Fatal(err)}
				stdout=path+"\n"
			}
			files,err:=cmd.VerifyTransferResult(ctx,[]byte(stdout))
			if err!=nil{t.Fatal(err)}
			observeCalls:=0
			observe:=func(_ context.Context,actual workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){
				observeCalls++
				if actual!=g||mode=="unconfirmed"{return nil,nil,errors.New("no writer proof")}
				return manifest,files,nil
			}
			sealedPath:=filepath.Join(stage,q.OperationID,"sealed")
			if mode=="partial marker"{
				if err:=os.Mkdir(sealedPath,0700);err!=nil{t.Fatal(err)}
			}
			if mode=="changed source"{
				if err:=os.WriteFile(path,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			}
			e,err:=a.Seal(ctx,g,observe)
			if mode=="unconfirmed"||mode=="partial marker"||mode=="changed source"{
				if err==nil||e.Version!=0{t.Fatal("unsafe seal published")}
				if mode=="unconfirmed"{
					if _,err:=os.Lstat(sealedPath);!errors.Is(err,os.ErrNotExist){t.Fatal("no-proof seal touched disk",err)}
				}else{
					if _,err:=os.Lstat(sealedPath);err!=nil{t.Fatal("partial state deleted",err)}
					if _,err:=a.Allocate(ctx,q);err==nil{t.Fatal("ambiguous state reallocated")}
				}
				return
			}
			if err!=nil||e.Allocation!=g||!slices.Equal(e.Files,files){t.Fatalf("seal %+v %v",e,err)}
			if _,err:=a.Workspace(ctx,g);err==nil{t.Fatal("sealed workspace reopened for write")}
			if _,err:=a.Seal(ctx,g,observe);err==nil||observeCalls!=1{t.Fatal("seal replay reissued")}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth)
			if err!=nil{t.Fatal(err)}
			if got,err:=a.Allocate(ctx,q);err!=nil||got!=g{t.Fatal("lost allocation reply",err)}
			switch mode{
			case "changed sealed bytes":
				if err:=os.WriteFile(filepath.Join(sealedPath,"media","42.jpg"),[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "changed sealed manifest":
				if err:=os.WriteFile(filepath.Join(sealedPath,"manifest.json"),[]byte(strings.Replace(string(manifest),"fixture","violence",1)),0600);err!=nil{t.Fatal(err)}
			case "revoked after seal":allowed=false
			}
			got,err:=a.Sealed(ctx,g)
			if mode=="valid"||mode=="empty"{
				if err!=nil||got.Allocation!=g||!slices.Equal(got.Files,e.Files){t.Fatalf("reopen %+v %v",got,err)}
				if len(files)>0{
					if b,err:=os.ReadFile(path);err!=nil||string(b)!=string(data){t.Fatal("original workspace altered",err)}
				}
			}else if err==nil||got.Version!=0{t.Fatal("changed/revoked sealed evidence accepted")}
		})
	}
}
