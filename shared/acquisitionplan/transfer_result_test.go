package acquisitionplan

import(
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func TestVerifyStrictTransferResult(t *testing.T){
	ctx:=context.Background()
	for _,mode:=range []string{"valid","empty","changed tags","changed media","wrong ID","missing sidecar","extra file","duplicate stdout","outside path","symlink","canceled"}{
		t.Run(mode,func(t *testing.T){
			manifest:=[]byte(`{"42":{"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg"}}`)
			sum:=sha256.Sum256(manifest)
			q:=workerproto.TransferPlanRequest{Metadata:workerproto.MetadataPlanRequest{JobID:1,Gallery:"A",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:2},Subcategory:"search",PolicyGeneration:1,ManifestSHA256:hex.EncodeToString(sum[:])}
			root,err:=filepath.EvalSymlinks(t.TempDir())
			if err!=nil{t.Fatal(err)}
			c,err:=PrepareTransferCommands(q,manifest,MetadataConfig{Generation:1,Source:q.Metadata.Source},root)
			if err!=nil{t.Fatal(err)}
			path:=filepath.Join(c.mediaRoot,"42.jpg")
			data:=[]byte("fixture bytes")
			sidecar:=`{"id":42,"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg","description":"not evidence"}`
			stdout:=path+"\n"
			write:=func(path string,b []byte){t.Helper();if err:=os.WriteFile(path,b,0600);err!=nil{t.Fatal(err)}}
			switch mode{
			case "changed tags":sidecar=strings.Replace(sidecar,"fixture","violence",1)
			case "changed media":sidecar=strings.Replace(sidecar,"/42.jpg","/99.jpg",1)
			case "wrong ID":sidecar=strings.Replace(sidecar,`"id":42`,`"id":43`,1)
			case "extra file":write(filepath.Join(c.mediaRoot,"extra.jpg"),data)
			case "duplicate stdout":stdout+=stdout
			case "outside path":stdout=filepath.Join(root,"42.jpg")+"\n"
			}
			if mode!="empty"{
				if mode=="symlink"{
					outside:=filepath.Join(root,"outside")
					write(outside,data)
					if err:=os.Symlink(outside,path);err!=nil{t.Skipf("symlink unavailable: %v",err)}
				}else{write(path,data)}
				if mode!="missing sidecar"{write(path+".json",[]byte(sidecar))}
			}else{stdout=""}
			run:=ctx
			if mode=="canceled"{c,cancel:=context.WithCancel(ctx);cancel();run=c}
			got,err:=c.VerifyTransferResult(run,[]byte(stdout))
			if mode=="valid"{
				digest:=sha256.Sum256(data)
				if err!=nil||len(got)!=1||got[0].ID!="42"||got[0].SHA256!=hex.EncodeToString(digest[:])||got[0].Size!=int64(len(data)){t.Fatalf("result %+v %v",got,err)}
			}else if mode=="empty"{
				if err!=nil||len(got)!=0{t.Fatal("empty legitimate result",err)}
			}else if got!=nil||err==nil{t.Fatalf("accepted %s %+v",mode,got)}
		})
	}
}
