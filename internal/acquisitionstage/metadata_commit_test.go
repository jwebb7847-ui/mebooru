package acquisitionstage

import(
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

func TestMappedMetadataUsesExistingLibraryServices(t *testing.T){
	for _,mode:=range []string{"new","duplicate rating","rejected tag","withheld tag","failure after source","unbound","wrong scope","lost result","changed source URL","missing tag attribution","changed rating","damaged result"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,auth,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){_ = a.Close()}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"},WithheldTags:[]string{"held"}}
			if mode!="unbound"{
				body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
				if err!=nil{t.Fatal(err)}
				if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			g,err:=library.Open(cfg)
			if err!=nil{t.Fatal(err)}
			defer func(){g.Close()}()
			if mode=="duplicate rating"||mode=="withheld tag"{
				path:=filepath.Join(s.GalleryRoot,"existing.png")
				if err:=os.WriteFile(path,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				landed,err:=g.LandInPlace(ctx,path,library.LandSpec{Origin:"fixture"})
				if err!=nil{t.Fatal(err)}
				id:=landed.Image.ID
				raw:=[]string{"rating:explicit"}
				if mode=="withheld tag"{raw=append(raw,"held","retired")}
				if _,_,err:=gallery.MergeSource(g.DB,g.TagSvc,id,row.SourceLabel,row.PostID,row.PostURL,"","",gallery.PostFile{},raw,gallery.Withheld{});err!=nil{t.Fatal(err)}
			}
			if mode=="rejected tag"{
				if _,err:=g.TagSvc.RejectTagName("fixture",g.GeneralCategoryID);err!=nil{t.Fatal(err)}
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			if err:=a.beginLanding(ctx,grant,g);err!=nil{t.Fatal(err)}
			res,err:=g.LandInPlace(ctx,filepath.Join(s.GalleryRoot,finalName(grant,f)),library.LandSpec{Origin:"fixture"})
			if err!=nil{t.Fatal(err)}
			if mode=="failure after source"{
				if _,err:=g.DB.Write.Exec(`CREATE TRIGGER fixture_refuse_tag BEFORE INSERT ON image_tag_sources BEGIN SELECT RAISE(ABORT,'fixture failure'); END`);err!=nil{t.Fatal(err)}
			}
			if mode=="wrong scope"{a.authority=func(context.Context,workerproto.StageAllocateRequest)(Scope,error){return Scope{},ErrStageRefused}}
			got,err:=a.applyMappedMetadata(ctx,grant,g)
			marker:=filepath.Join(stage,grant.Request.OperationID,"publication","metadata-attempt.json")
			switch mode{
			case "unbound","wrong scope":
				if err==nil||got!=nil{t.Fatal("unauthorized metadata")}
				if _,err:=os.Stat(marker);!os.IsNotExist(err){t.Fatal("unexpected marker",err)}
				return
			case "failure after source":
				if err==nil||got!=nil{t.Fatal("partial merge success")}
				var count int
				if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_sources WHERE image_id=? AND site=? AND post_id=?`,res.Image.ID,row.SourceLabel,row.PostID).Scan(&count);err!=nil||count!=1{t.Fatal("fixture did not reach partial write",err)}
				if _,err:=g.DB.Write.Exec(`DROP TRIGGER fixture_refuse_tag`);err!=nil{t.Fatal(err)}
			default:
				want:="general"
				if mode=="duplicate rating"||mode=="withheld tag"{want="explicit"}
				if err!=nil||len(got)!=1||got[0].ImageID!=res.Image.ID||got[0].Rating!=want{t.Fatalf("metadata %+v %v",got,err)}
				if (mode=="rejected tag")!=slices.Contains(got[0].Rejected,"fixture"){t.Fatal("rejection disposition",got)}
				if mode=="withheld tag"{
					for name,want:=range map[string]int{"held":0,"retired":1}{
						var stale int
						if err:=g.DB.Read.QueryRow(`SELECT it.stale FROM image_tags it JOIN tags t ON t.id=it.tag_id WHERE it.image_id=? AND t.name=?`,res.Image.ID,name).Scan(&stale);err!=nil||stale!=want{t.Fatal("withheld/stale",name,stale,err)}
					}
				}
			}
			if _,err:=os.Stat(marker);err!=nil{t.Fatal("missing attempt",err)}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
			if got,err:=a.applyMappedMetadata(ctx,grant,g);err==nil||got!=nil{t.Fatal("metadata attempt replayed")}
			if _,err:=a.observeLanding(ctx,grant,g);err!=nil{t.Fatal("landing evidence lost",err)}
			resultPath:=filepath.Join(filepath.Dir(marker),"metadata-result.json")
			switch mode{
			case "lost result":
				if err:=os.Remove(resultPath);err!=nil{t.Fatal(err)}
			case "changed source URL":
				if _,err:=g.DB.Write.Exec(`UPDATE image_sources SET url='https://example.invalid/different' WHERE image_id=?`,res.Image.ID);err!=nil{t.Fatal(err)}
			case "missing tag attribution":
				if _,err:=g.DB.Write.Exec(`DELETE FROM image_tag_sources WHERE image_id=?`,res.Image.ID);err!=nil{t.Fatal(err)}
			case "changed rating":
				cat,name,err:=gallery.ResolveCategoryTag(g.DB,"rating:explicit");if err!=nil{t.Fatal(err)}
				tag,err:=g.TagSvc.GetOrCreateTag(name,cat);if err!=nil{t.Fatal(err)}
				if _,err:=g.TagSvc.AddTagsToOneImage(res.Image.ID,[]int64{tag.ID},"user");err!=nil{t.Fatal(err)}
			case "damaged result":
				if err:=os.WriteFile(resultPath,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			}
			reconciled,err:=a.observeMappedMetadata(ctx,grant,g)
			switch mode{
			case "failure after source","lost result","changed source URL","missing tag attribution","changed rating","damaged result":
				if err==nil||reconciled!=nil{t.Fatal("uncertain or changed metadata reconciled")}
			default:
				if err!=nil||len(reconciled)!=1||!sameMetadataObservation(got[0],reconciled[0]){t.Fatalf("lost reply %+v %v",reconciled,err)}
			}
		})
	}
}
