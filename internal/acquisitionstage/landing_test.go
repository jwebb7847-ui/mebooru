package acquisitionstage

import(
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/library"
)

func TestPlacedMediaLandingObservationIsNotReceipt(t *testing.T){
	for _,mode:=range []string{"before DB write","actual ingest","existing duplicate","preexisting path","changed DB row","different DB","changed placed bytes"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			pic:=image.NewRGBA(image.Rect(0,0,16,16))
			pic.Set(4,4,color.RGBA{R:255,A:255})
			if err:=png.Encode(&buf,pic);err!=nil{t.Fatal(err)}
			a,stage,s,grant,auth,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){_ = a.Close()}()
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			g,err:=library.Open(cfg)
			if err!=nil{t.Fatal(err)}
			defer func(){g.Close()}()
			if mode=="existing duplicate"{
				path:=filepath.Join(s.GalleryRoot,"existing.png")
				if err:=os.WriteFile(path,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				if _,err:=g.LandInPlace(ctx,path,library.LandSpec{Origin:"fixture"});err!=nil{t.Fatal(err)}
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			path:=filepath.Join(s.GalleryRoot,finalName(grant,f))
			if mode=="preexisting path"{
				if _,err:=g.LandInPlace(ctx,path,library.LandSpec{Origin:"fixture"});err!=nil{t.Fatal(err)}
				if err:=a.beginLanding(ctx,grant,g);err==nil{t.Fatal("preexisting row attributed to operation")}
				return
			}
			if _,err:=a.observeLanding(ctx,grant,g);err==nil{t.Fatal("unattempted landing observed")}
			if err:=a.beginLanding(ctx,grant,g);err!=nil{t.Fatal(err)}
			if err:=a.beginLanding(ctx,grant,g);err==nil{t.Fatal("landing attempt replay")}
			before,err:=a.observeLanding(ctx,grant,g)
			if err!=nil||len(before)!=1||before[0].State!="absent"||before[0].ImageID!=0{t.Fatalf("prewrite %+v %v",before,err)}
			var id int64
			if mode!="before DB write"{
				// Actual existing library implementation; intentionally
				// not invoked by production candidate. No source/rating/
				// tag/provenance writes here, no final import success.
				res,err:=g.LandInPlace(ctx,path,library.LandSpec{Origin:"fixture",MaxBytes:1024})
				if err!=nil{t.Fatal(err)}
				id=res.Image.ID
				if (mode=="existing duplicate")!=res.Duplicate{t.Fatal("duplicate disposition")}
			}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
			g.Close()
			if mode=="different DB"{cfg.DBPath=filepath.Join(filepath.Dir(stage),"other.sqlite")}
			g,err=library.Open(cfg);if err!=nil{t.Fatal(err)}
			switch mode{
			case "changed DB row":
				if _,err:=g.DB.Write.Exec(`UPDATE images SET file_size=file_size+1 WHERE id=?`,id);err!=nil{t.Fatal(err)}
			case "changed placed bytes":
				if err:=os.WriteFile(path,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			}
			got,err:=a.observeLanding(ctx,grant,g)
			switch mode{
			case "before DB write":
				if err!=nil||len(got)!=1||got[0].State!="absent"{t.Fatalf("uncertain absent %+v %v",got,err)}
			case "actual ingest","existing duplicate":
				if err!=nil||len(got)!=1||got[0].State!="observed_path"||got[0].ImageID!=id||got[0].Canonical!=(mode!="existing duplicate"){t.Fatalf("observed %+v %v",got,err)}
			default:
				if err==nil||got!=nil{t.Fatal("changed state accepted")}
			}
			if err:=a.beginLanding(ctx,grant,g);err==nil{t.Fatal("restart reauthorized ingestion")}
		})
	}
}
