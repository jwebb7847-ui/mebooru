package acquisitionstage

import(
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func digest(b []byte)string{
	sum:=sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestCopySealedMediaExactEvidence(t *testing.T){
	for _,mode:=range []string{"valid","empty","changed media","changed sidecar","changed metadata with updated hash","extra file","missing sidecar","duplicate result","unapproved ID","traversal","bad extension","symlink","hardlink","destination collision","budget","canceled"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			stage,s,q:=fixture(t)
			manifest:=[]byte(`{"42":{"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg"}}`)
			q.Transfer.ManifestSHA256=digest(manifest)
			s.MaxBytes=2048
			a,err:=Open(stage,1,2048,func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return s,nil})
			if err!=nil{t.Fatal(err)}
			defer a.Close()
			grant,err:=a.Allocate(ctx,q)
			if err!=nil{t.Fatal(err)}
			workspace,err:=a.Workspace(ctx,grant)
			if err!=nil{t.Fatal(err)}
			// Use the existing strict result verifier to obtain the exact
			// media and sidecar identities from a synthetic transfer.
			commands,err:=acquisitionplan.PrepareTransferCommands(q.Transfer,manifest,acquisitionplan.MetadataConfig{Generation:1,Source:q.Transfer.Metadata.Source},workspace)
			if err!=nil{t.Fatal(err)}
			srcPath:=filepath.Join(workspace,"media")
			file:=filepath.Join(srcPath,"42.jpg")
			data:=[]byte("fixture media")
			meta:=[]byte(`{"id":42,"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg","description":"native metadata"}`)
			write:=func(path string,b []byte){t.Helper();if err:=os.WriteFile(path,b,0600);err!=nil{t.Fatal(err)}}
			stdout:=""
			if mode!="empty"{write(file,data);write(file+".json",meta);stdout=file+"\n"}
			files,err:=commands.VerifyTransferResult(ctx,[]byte(stdout))
			if err!=nil{t.Fatal(err)}
			dstPath:=filepath.Join(filepath.Dir(stage),"sealed")
			if err:=os.Mkdir(dstPath,0700);err!=nil{t.Fatal(err)}
			switch mode{
			case "changed media":write(file,[]byte("changed media"))
			case "changed sidecar":write(file+".json",[]byte(strings.Replace(string(meta),"native metadata","edited metadata",1)))
			case "changed metadata with updated hash":
				changed:=[]byte(strings.Replace(string(meta),"fixture","violence",1))
				write(file+".json",changed)
				files[0].SidecarSize=int64(len(changed));files[0].SidecarSHA256=digest(changed)
			case "extra file":write(filepath.Join(srcPath,"extra.jpg"),data)
			case "missing sidecar":if err:=os.Remove(file+".json");err!=nil{t.Fatal(err)}
			case "duplicate result":files=append(files,files[0])
			case "unapproved ID":
				for _,suffix:=range []string{"",".json"}{
					if err:=os.Rename(file+suffix,filepath.Join(srcPath,"43.jpg"+suffix));err!=nil{t.Fatal(err)}
				}
				files[0].ID="43";files[0].RelativeName="43.jpg"
			case "traversal":files[0].RelativeName="../42.jpg"
			case "bad extension":files[0].RelativeName="42.exe"
			case "symlink","hardlink":
				outside:=filepath.Join(filepath.Dir(stage),"outside")
				write(outside,data)
				if err:=os.Remove(file);err!=nil{t.Fatal(err)}
				if mode=="symlink"{err=os.Symlink(outside,file)}else{err=os.Link(outside,file)}
				if err!=nil{t.Skipf("link unavailable: %v",err)}
			case "destination collision":write(filepath.Join(dstPath,"42.jpg"),[]byte("do not overwrite"))
			case "budget":grant.MaxBytes=uint64(len(data))
			case "canceled":c,cancel:=context.WithCancel(ctx);cancel();ctx=c
			}
			src,err:=os.OpenRoot(srcPath);if err!=nil{t.Fatal(err)};defer src.Close()
			dst,err:=os.OpenRoot(dstPath);if err!=nil{t.Fatal(err)};defer dst.Close()
			got,err:=copySealedMedia(ctx,grant,manifest,files,src,dst)
			switch mode{
			case "valid":
				if err!=nil||len(got)!=1||got[0]!=files[0]{t.Fatalf("copy %+v %v",got,err)}
				if b,err:=os.ReadFile(filepath.Join(dstPath,"42.jpg"));err!=nil||string(b)!=string(data){t.Fatal("media changed",err)}
				if b,err:=os.ReadFile(filepath.Join(dstPath,"42.jpg.json"));err!=nil||string(b)!=string(meta){t.Fatal("sidecar changed",err)}
			case "empty":if err!=nil||len(got)!=0{t.Fatal("empty approved transfer",err)}
			default:
				if err==nil||got!=nil{t.Fatalf("accepted %s %+v",mode,got)}
				if mode=="budget"&&!errors.Is(err,ErrStageLimit){t.Fatal("budget",err)}
				if mode=="destination collision"{
					if b,err:=os.ReadFile(filepath.Join(dstPath,"42.jpg"));err!=nil||string(b)!="do not overwrite"{t.Fatal("collision overwritten",err)}
				}
			}
		})
	}
}
