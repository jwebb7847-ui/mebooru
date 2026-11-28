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
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)
func TestRestoreExistingIdentityThroughActualIngest(t *testing.T){
	for _,mode:=range []string{"marked missing","unmarked missing","still held","old alias","old path returns","changed after restore"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,auth,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){_ = a.Close()}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"},
				Provenance:&acquisitionplan.ApprovedProvenance{}}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			g,err:=library.Open(config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")})
			if err!=nil{t.Fatal(err)}
			defer g.Close()
			old:=filepath.Join(s.GalleryRoot,"existing.png")
			if err:=os.WriteFile(old,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			res,err:=g.LandInPlace(ctx,old,library.LandSpec{Origin:"fixture"})
			if err!=nil{t.Fatal(err)}
			id:=res.Image.ID
			if _,_,err:=gallery.MergeSource(g.DB,g.TagSvc,id,"other","7","https://example.invalid/7","","",gallery.PostFile{},[]string{"rating:explicit","prior"},gallery.Withheld{});err!=nil{t.Fatal(err)}
			if mode=="old alias"{
				alias:=filepath.Join(s.GalleryRoot,"operator-copy.png")
				if err:=os.WriteFile(alias,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				if _,err:=g.LandInPlace(ctx,alias,library.LandSpec{Origin:"fixture"});err!=nil{t.Fatal(err)}
			}
			if mode!="still held"{
				if err:=os.Remove(old);err!=nil{t.Fatal(err)}
				if mode!="unmarked missing"{if _,err:=gallery.SetMissing(g.DB,id,true);err!=nil{t.Fatal(err)}}
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			err=a.landRestoredPrepared(ctx,grant,g)
			if mode=="still held"||mode=="old alias"{
				if err==nil{t.Fatal("unqualified restoration accepted")}
				return
			}
			if err!=nil{t.Fatal(err)}
			if err:=a.landRestoredPrepared(ctx,grant,g);err==nil{t.Fatal("restoration replayed")}
			if mode=="old path returns"{
				if err:=os.WriteFile(old,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				if _,err:=a.applyMappedMetadata(ctx,grant,g);err==nil{t.Fatal("returned old path ignored")}
				return
			}
			v,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(v)!=1||v[0].ImageID!=id||v[0].Rating!="explicit"{t.Fatalf("restore metadata %+v %v",v,err)}
			if _,err:=a.disposeHeldDuplicate(ctx,grant,g);err==nil{t.Fatal("restored canonical disposed")}
			var n int
			if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_tags it JOIN tags t ON t.id=it.tag_id WHERE it.image_id=? AND t.name='prior'`,id).Scan(&n);err!=nil||n!=1{t.Fatal("prior tag lost",err)}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
			if mode=="changed after restore"{
				if _,err:=g.DB.Write.Exec(`UPDATE images SET is_missing=1 WHERE id=?`,id);err!=nil{t.Fatal(err)}
			}
			got,err:=a.observeMappedMetadata(ctx,grant,g)
			if mode=="changed after restore"{if err==nil{t.Fatal("changed restoration accepted")}}else if err!=nil||len(got)!=1||got[0].ImageID!=id{t.Fatal("restore observation",got,err)}
		})
	}
}
