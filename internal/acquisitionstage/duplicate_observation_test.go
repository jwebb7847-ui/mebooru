package acquisitionstage

import(
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/library"
)
func TestHeldDuplicateCanonicalObservation(t *testing.T){
	for _,mode:=range []string{"held","nested","no metadata","changed canonical","missing canonical","wrong canonical path","extra hardlink","new image","revoked","dispose","DB delete failure","changed after disposal","user alias preserved","revoked after marker"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,_,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"},
				Provenance:&acquisitionplan.ApprovedProvenance{Original:"https://example.invalid/upstream"}}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			g,err:=library.Open(config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")})
			if err!=nil{t.Fatal(err)}
			defer g.Close()
			old:=filepath.Join(s.GalleryRoot,"existing.png")
			if mode=="nested"{
				old=filepath.Join(s.GalleryRoot,"nested","existing.png")
				if err:=os.MkdirAll(filepath.Dir(old),0700);err!=nil{t.Fatal(err)}
			}
			var id int64
			if mode!="new image"{
				if err:=os.WriteFile(old,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,old,library.LandSpec{Origin:"fixture"})
				if err!=nil{t.Fatal(err)}
				id=res.Image.ID
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			if err:=a.beginLanding(ctx,grant,g);err!=nil{t.Fatal(err)}
			alias:=filepath.Join(s.GalleryRoot,finalName(grant,f))
			res,err:=g.LandInPlace(ctx,alias,library.LandSpec{Origin:"fixture"})
			if err!=nil||res.Duplicate!=(mode!="new image"){t.Fatal("landing",err)}
			if mode!="new image"&&res.Image.ID!=id{t.Fatal("different canonical ID")}
			if mode!="no metadata"{
				if _,err:=a.applyMappedMetadata(ctx,grant,g);err!=nil{t.Fatal(err)}
			}
			switch mode{
			case "changed canonical":
				if err:=os.WriteFile(old,bytes.Repeat([]byte{'x'},buf.Len()),0600);err!=nil{t.Fatal(err)}
			case "missing canonical":
				if err:=os.Remove(old);err!=nil{t.Fatal(err)}
			case "wrong canonical path":
				if _,err:=g.DB.Write.Exec(`UPDATE images SET canonical_path=? WHERE id=?`,filepath.Join(filepath.Dir(stage),"outside.png"),id);err!=nil{t.Fatal(err)}
			case "extra hardlink":
				if err:=os.Link(old,filepath.Join(s.GalleryRoot,"user-copy.png"));err!=nil{t.Fatal(err)}
			case "revoked":
				a.authority=func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return Scope{},ErrStageRefused}
			}
			if mode=="dispose"||mode=="DB delete failure"||mode=="changed after disposal"||mode=="user alias preserved"||mode=="revoked after marker"{
				userAlias:=filepath.Join(s.GalleryRoot,"operator-copy.png")
				if mode=="user alias preserved"{
					if err:=os.WriteFile(userAlias,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
					if res,err:=g.LandInPlace(ctx,userAlias,library.LandSpec{Origin:"fixture"});err!=nil||!res.Duplicate{t.Fatal("user alias",err)}
				}
				if mode=="revoked after marker"{
					a.authority=func(context.Context,workerproto.StageAllocateRequest)(Scope,error){
						if _,err:=os.Stat(filepath.Join(stage,grant.Request.OperationID,"publication","duplicate-attempt.json"));err==nil{return Scope{},ErrStageRefused}
						return s,nil
					}
					if _,err:=a.disposeHeldDuplicate(ctx,grant,g);err==nil{t.Fatal("revoked disposal")}
					if b,err:=os.ReadFile(alias);err!=nil||!bytes.Equal(b,buf.Bytes()){t.Fatal("revocation removed alias",err)}
					a.authority=func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return s,nil}
					if _,err:=a.disposeHeldDuplicate(ctx,grant,g);err==nil{t.Fatal("revoked attempt replayed")}
					return
				}
				if mode=="DB delete failure"{
					if _,err:=g.DB.Write.Exec(`CREATE TRIGGER refuse_alias_delete BEFORE DELETE ON image_paths BEGIN SELECT RAISE(ABORT,'fixture failure'); END`);err!=nil{t.Fatal(err)}
				}
				got,err:=a.disposeHeldDuplicate(ctx,grant,g)
				if mode=="DB delete failure"{if err==nil{t.Fatal("partial deletion succeeded")}}else if err!=nil||got.ImageID!=id{t.Fatal("disposal",got,err)}
				if _,err:=os.Lstat(alias);!os.IsNotExist(err){t.Fatal("managed alias remains",err)}
				if b,err:=os.ReadFile(old);err!=nil||!bytes.Equal(b,buf.Bytes()){t.Fatal("canonical changed",err)}
				if _,err:=a.disposeHeldDuplicate(ctx,grant,g);err==nil{t.Fatal("disposal replayed")}
				if mode=="changed after disposal"{if err:=os.WriteFile(old,[]byte("changed"),0600);err!=nil{t.Fatal(err)}}
				if err:=a.Close();err!=nil{t.Fatal(err)}
				a,err=Open(stage,2,4096,func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return s,nil})
				if err!=nil{t.Fatal(err)}
				got,err=a.observeDuplicateDisposal(ctx,grant,g)
				if mode=="dispose"||mode=="user alias preserved"{if err!=nil||got.ImageID!=id{t.Fatal("lost response",got,err)}}else if err==nil{t.Fatal("uncertain deletion reconciled")}
				if mode=="user alias preserved"{
					if b,err:=os.ReadFile(userAlias);err!=nil||!bytes.Equal(b,buf.Bytes()){t.Fatal("operator copy lost",err)}
					var n int
					if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND path=? AND is_canonical=0`,id,userAlias).Scan(&n);err!=nil||n!=1{t.Fatal("operator alias row lost",err)}
				}
				return
			}
			got,err:=a.observeHeldDuplicates(ctx,grant,g)
			if mode=="held"||mode=="nested"{
				rel,errRel:=filepath.Rel(s.GalleryRoot,old)
				if err!=nil||errRel!=nil||len(got)!=1||got[0].ImageID!=id||got[0].CanonicalPath!=rel||got[0].SHA256!=f.SHA256||got[0].CanonicalIdentity==""{t.Fatalf("held duplicate %+v %v",got,err)}
			}else if err==nil||got!=nil{t.Fatal("unsafe duplicate observed")}
			// No deletion of a landed alias or existing canonical by the
			// read-only witness. Actual duplicate disposal is separate.
			if b,err:=os.ReadFile(alias);err!=nil||!bytes.Equal(b,buf.Bytes()){t.Fatal("alias modified",err)}
			var count int
			if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_paths WHERE path=?`,alias).Scan(&count);err!=nil||count!=1{t.Fatal("alias record removed",err)}
		})
	}
}
