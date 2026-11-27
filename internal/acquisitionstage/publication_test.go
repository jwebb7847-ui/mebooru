package acquisitionstage

import(
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func publicationFixture(t *testing.T)(*Allocator,string,Scope,workerproto.StageAllocation,Authority,acquisitionplan.VerifiedTransferFile){
	t.Helper()
	return publicationFixtureData(t,"jpg",[]byte("fixture bytes"))
}
func publicationFixtureData(t *testing.T,ext string,data []byte)(*Allocator,string,Scope,workerproto.StageAllocation,Authority,acquisitionplan.VerifiedTransferFile){
	t.Helper()
	ctx:=context.Background()
	stage,s,q:=fixture(t)
	manifest:=[]byte(`{"42":{"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg"}}`)
	manifest=[]byte(strings.ReplaceAll(string(manifest),"42.jpg","42."+ext))
	q.Transfer.ManifestSHA256=digest(manifest)
	s.MaxBytes=2048
	auth:=func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return s,nil}
	a,err:=Open(stage,2,4096,auth)
	if err!=nil{t.Fatal(err)}
	g,err:=a.Allocate(ctx,q)
	if err!=nil{a.Close();t.Fatal(err)}
	w,err:=a.Workspace(ctx,g)
	if err!=nil{a.Close();t.Fatal(err)}
	cmd,err:=acquisitionplan.PrepareTransferCommands(q.Transfer,manifest,acquisitionplan.MetadataConfig{Generation:1,Source:q.Transfer.Metadata.Source},w)
	if err!=nil{a.Close();t.Fatal(err)}
	path:=filepath.Join(w,"media","42."+ext)
	meta:=[]byte(`{"id":42,"category":"derpibooru","subcategory":"search","tags":["safe","fixture"],"meloader_verified_media_url":"https://example.invalid/42.jpg"}`)
	meta=[]byte(strings.ReplaceAll(string(meta),"42.jpg","42."+ext))
	for p,b:=range map[string][]byte{path:data,path+".json":meta}{if err:=os.WriteFile(p,b,0600);err!=nil{a.Close();t.Fatal(err)}}
	files,err:=cmd.VerifyTransferResult(ctx,[]byte(path+"\n"))
	if err!=nil||len(files)!=1{a.Close();t.Fatal("verify",err)}
	if _,err:=a.Seal(ctx,g,func(context.Context,workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){return manifest,files,nil});err!=nil{a.Close();t.Fatal(err)}
	return a,stage,s,g,auth,files[0]
}
func TestPublicationIntentAndNoClobber(t *testing.T){
	ctx:=context.Background()
	for _,mode:=range []string{"placed","prepared restart","attempt without file","same bytes collision","different collision","collision after marker","changed prepared","partial intent","changed placed","foreign link","low space","canceled"}{
		t.Run(mode,func(t *testing.T){
			a,stage,s,g,auth,f:=publicationFixture(t)
			defer func(){_ = a.Close()}()
			p:=filepath.Join(stage,g.Request.OperationID,"publication")
			name:=finalName(g,f)
			final:=filepath.Join(s.GalleryRoot,name)
			if mode=="low space"{a.freeBytes=func(string)(uint64,error){return 0,nil}}
			if mode=="partial intent"{if err:=os.Mkdir(p,0700);err!=nil{t.Fatal(err)}}
			err:=a.preparePublication(ctx,g)
			if mode=="low space"||mode=="partial intent"{
				if err==nil{t.Fatal("unsafe preparation")}
				if mode=="partial intent"{if _,err:=a.Allocate(ctx,g.Request);err==nil{t.Fatal("partial intent ignored")}}
				return
			}
			if err!=nil{t.Fatal(err)}
			if _,err:=os.Lstat(final);!errors.Is(err,os.ErrNotExist){t.Fatal("preparation exposed gallery file")}
			out,err:=a.inspectPublication(ctx,g)
			if err!=nil||len(out)!=1||out[0].State!="prepared"{t.Fatalf("prepared %+v %v",out,err)}
			if err:=a.preparePublication(ctx,g);err==nil{t.Fatal("reprepared intent")}
			if mode=="prepared restart"||mode=="attempt without file"{
				if mode=="attempt without file"{
					root,err:=os.OpenRoot(p);if err!=nil{t.Fatal(err)}
					err=writeOwnerBytes(ctx,root,"attempt",[]byte(g.TransferID));root.Close()
					if err!=nil{t.Fatal(err)}
				}
				if err:=a.Close();err!=nil{t.Fatal(err)}
				a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
				out,err=a.inspectPublication(ctx,g)
				want:="prepared";if mode=="attempt without file"{want="absent"}
				if err!=nil||len(out)!=1||out[0].State!=want{t.Fatalf("restart %+v %v",out,err)}
				if mode=="attempt without file"{
					if _,err:=a.publishPrepared(ctx,g);err==nil{t.Fatal("ambiguous attempt retried")}
					return
				}
			}
			switch mode{
			case "same bytes collision","different collision":
				data:=[]byte("fixture bytes");if mode=="different collision"{data=[]byte("existing")}
				if err:=os.WriteFile(final,data,0600);err!=nil{t.Fatal(err)}
				if _,err:=a.publishPrepared(ctx,g);err==nil{t.Fatal("collision accepted")}
				if got,err:=os.ReadFile(final);err!=nil||string(got)!=string(data){t.Fatal("collision overwritten",err)}
				if _,err:=a.inspectPublication(ctx,g);err==nil{t.Fatal("collision mistaken for placement")}
				return
			case "collision after marker":
				a.authority=func(c context.Context,q workerproto.StageAllocateRequest)(Scope,error){
					if _,err:=os.Stat(filepath.Join(p,"attempt"));err==nil{
						if _,err:=os.Stat(final);errors.Is(err,os.ErrNotExist){
							if err:=os.WriteFile(final,[]byte("racing destination"),0600);err!=nil{t.Fatal(err)}
						}
					}
					return auth(c,q)
				}
				if _,err:=a.publishPrepared(ctx,g);err==nil{t.Fatal("atomic link overwrote race")}
				if got,err:=os.ReadFile(final);err!=nil||string(got)!="racing destination"{t.Fatal("racing file changed",err)}
				if _,err:=os.Stat(filepath.Join(p,"attempt"));err!=nil{t.Fatal("race not exercised after marker",err)}
				return
			case "changed prepared":
				if err:=os.WriteFile(filepath.Join(p,"media",f.RelativeName),[]byte("wrong bytes!!"),0600);err!=nil{t.Fatal(err)}
				if _,err:=a.publishPrepared(ctx,g);err==nil{t.Fatal("changed bytes published")}
				return
			case "canceled":
				c,cancel:=context.WithCancel(ctx);cancel()
				if _,err:=a.publishPrepared(c,g);err==nil{t.Fatal("canceled publication")}
				return
			}
			out,err=a.publishPrepared(ctx,g)
			if err!=nil||len(out)!=1||out[0].State!="placed"{t.Fatalf("publish %+v %v",out,err)}
			if _,err:=a.publishPrepared(ctx,g);err==nil{t.Fatal("attempt replayed")}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
			if got,err:=os.ReadFile(final);err!=nil||string(got)!="fixture bytes"{t.Fatal("published bytes",err)}
			if mode=="changed placed"{if err:=os.WriteFile(final,[]byte("wrong bytes!!"),0600);err!=nil{t.Fatal(err)}}
			if mode=="foreign link"{if err:=os.Link(final,filepath.Join(s.GalleryRoot,"unexpected.jpg"));err!=nil{t.Fatal(err)}}
			out,err=a.inspectPublication(ctx,g)
			if mode=="changed placed"||mode=="foreign link"{
				if err==nil||out!=nil{t.Fatal("changed placement reconciled")}
			}else if err!=nil||len(out)!=1||out[0].State!="placed"{t.Fatalf("lost reply %+v %v",out,err)}
			// Sealed evidence is independent of the publication link.
			if _,err:=a.Sealed(ctx,g);err!=nil{t.Fatal("sealed evidence damaged",err)}
		})
	}
}
