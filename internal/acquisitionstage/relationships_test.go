package acquisitionstage

import (
	"bytes"
	"context"
	"image"
	"image/color"
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

func TestManagedRelationshipWitness(t *testing.T){
	for _,mode:=range []string{"parent","child","manual","missing","self","ambiguous","write failure","lost result","changed edge","revoked after attempt","unknown phase","orphan result","damaged attempt","damaged result"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			pngBytes:=func(c uint8)[]byte{
				t.Helper()
				pic:=image.NewRGBA(image.Rect(0,0,16,16))
				pic.Set(4,4,color.RGBA{R:c,A:255})
				var buf bytes.Buffer
				if err:=png.Encode(&buf,pic);err!=nil{t.Fatal(err)}
				return buf.Bytes()
			}
			a,stage,s,grant,auth,f:=publicationFixtureData(t,"png",pngBytes(1))
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			url,parentURL:="https://derpibooru.org/images/42","https://derpibooru.org/images/40"
			if mode=="child"{parentURL=""}
			if mode=="self"{parentURL=url}
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:url,Rating:"general",Tags:[]string{"fixture","rating:general"},
				Provenance:&acquisitionplan.ApprovedProvenance{ParentURL:parentURL}}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			g,err:=library.Open(config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")})
			if err!=nil{t.Fatal(err)}
			defer g.Close()
			land:=func(name string,c uint8)int64{
				t.Helper()
				p:=filepath.Join(s.GalleryRoot,name+".png")
				if err:=os.WriteFile(p,pngBytes(c),0600);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,p,library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("land",err)}
				return res.Image.ID
			}
			source:=func(id int64,post,url,parent string){
				t.Helper()
				if err:=gallery.ApplyCreateProvenance(g.DB,id,"derpibooru",post,url,"",parent,"","","","",gallery.PostFile{},nil);err!=nil{t.Fatal(err)}
			}
			var other,manual int64
			switch mode{
			case "missing","self":
			case "child":
				other=land("child",2)
				source(other,"43","https://derpibooru.org/images/43",url)
			default:
				other=land("parent",2)
				source(other,"40",parentURL,"")
			}
			if mode=="ambiguous"{source(land("second",3),"41",parentURL,"")}
			if mode=="manual"{manual=land("manual",4)}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			if err:=a.beginLanding(ctx,grant,g);err!=nil{t.Fatal(err)}
			res,err:=g.LandInPlace(ctx,filepath.Join(s.GalleryRoot,finalName(grant,f)),library.LandSpec{Origin:"fixture"})
			if err!=nil||res.Image==nil{t.Fatal(err)}
			id:=res.Image.ID
			if mode=="manual"{if err:=g.RelationsSvc.AddDerivativeEdge(manual,id);err!=nil{t.Fatal(err)}}
			if _,err:=a.applyMappedMetadata(ctx,grant,g);err!=nil{t.Fatal(err)}
			if mode=="write failure"{
				if _,err:=g.DB.Write.Exec(`CREATE TRIGGER fixture_refuse_edge BEFORE INSERT ON derivative_edges BEGIN SELECT RAISE(ABORT,'fixture failure'); END`);err!=nil{t.Fatal(err)}
			}
			dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
			if mode=="revoked after attempt"{
				a.authority=func(ctx context.Context,q workerproto.StageAllocateRequest)(Scope,error){
					if _,err:=os.Stat(filepath.Join(dir,"relationship-attempt.json"));err==nil{return Scope{},ErrStageRefused}
					return auth(ctx,q)
				}
			}
			err=a.applyRelationships(ctx,grant,g)
			if mode=="revoked after attempt"{
				if err==nil{t.Fatal("revoked relation succeeded")}
				a.authority=auth
				if _,err:=os.Stat(filepath.Join(dir,"relationship-attempt.json"));err!=nil{t.Fatal(err)}
				if _,err:=os.Stat(filepath.Join(dir,"relationship-result.json"));!os.IsNotExist(err){t.Fatal("unexpected result",err)}
			}
			if mode=="ambiguous"{
				if err==nil{t.Fatal("ambiguous parent accepted")}
				if _,err:=os.Stat(filepath.Join(dir,"relationship-attempt.json"));!os.IsNotExist(err){t.Fatal("ambiguous attempt",err)}
				return
			}
			if mode=="write failure"{
				if err==nil{t.Fatal("failed edge accepted")}
				if _,err:=os.Stat(filepath.Join(dir,"relationship-attempt.json"));err!=nil{t.Fatal(err)}
				if _,err:=g.DB.Write.Exec(`DROP TRIGGER fixture_refuse_edge`);err!=nil{t.Fatal(err)}
			}else if mode!="revoked after attempt"&&err!=nil{t.Fatal(err)}
			child,want:=id,other
			if mode=="child"{child,want=other,id}
			if mode=="manual"{want=manual}
			if mode=="missing"||mode=="self"||mode=="write failure"||mode=="revoked after attempt"{want=0}
			var count int
			if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM derivative_edges WHERE derivative_image_id=?`,child).Scan(&count);err!=nil{t.Fatal(err)}
			if want==0{if count!=0{t.Fatal("unexpected edge")}}else{
				var got int64
				if err:=g.DB.Read.QueryRow(`SELECT source_image_id FROM derivative_edges WHERE derivative_image_id=?`,child).Scan(&got);err!=nil||count!=1||got!=want{t.Fatal("wrong edge",got,want,err)}
			}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
			if err:=a.applyRelationships(ctx,grant,g);err==nil{t.Fatal("replayed attempt")}
			if mode=="lost result"{if err:=os.Remove(filepath.Join(dir,"relationship-result.json"));err!=nil{t.Fatal(err)}}
			if mode=="changed edge"{if _,err:=g.DB.Write.Exec(`DELETE FROM derivative_edges WHERE derivative_image_id=?`,child);err!=nil{t.Fatal(err)}}
			switch mode{
			case "unknown phase":
				if err:=os.WriteFile(filepath.Join(dir,"unknown.json"),[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "orphan result":
				if err:=os.Remove(filepath.Join(dir,"relationship-attempt.json"));err!=nil{t.Fatal(err)}
			case "damaged attempt":
				if err:=os.WriteFile(filepath.Join(dir,"relationship-attempt.json"),[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "damaged result":
				if err:=os.WriteFile(filepath.Join(dir,"relationship-result.json"),[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			}
			err=a.observeRelationships(ctx,grant,g)
			bad:=mode=="write failure"||mode=="lost result"||mode=="changed edge"||mode=="revoked after attempt"||mode=="unknown phase"||mode=="orphan result"||mode=="damaged attempt"||mode=="damaged result"
			if (err!=nil)!=bad{t.Fatal("unexpected reconciliation",err)}
		})
	}
}
