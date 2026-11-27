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

func TestManagedCollectionWitness(t *testing.T){
	for _,mode:=range []string{"new","duplicate home","same home","duplicate no home","existing label",
		"missing order","no relationship","write failure","revoked after attempt","lost result","changed position",
		"changed home","orphan result","damaged result"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			pngBytes:=func(c uint8)[]byte{
				t.Helper()
				pic:=image.NewRGBA(image.Rect(0,0,16,16))
				pic.Set(4,4,color.RGBA{R:c,A:255})
				var b bytes.Buffer
				if err:=png.Encode(&b,pic);err!=nil{t.Fatal(err)}
				return b.Bytes()
			}
			data:=pngBytes(1)
			a,stage,s,grant,auth,f:=publicationFixtureData(t,"png",data)
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			claim:=&acquisitionplan.ApprovedProvenance{Collection:"pool",CollectionOrder:7}
			if mode=="missing order"{claim.CollectionOrder=0}
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",
				Tags:[]string{"fixture","rating:general"},Provenance:claim}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			g,err:=library.Open(config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")})
			if err!=nil{t.Fatal(err)}
			defer g.Close()
			land:=func(name string,b []byte)int64{
				t.Helper()
				p:=filepath.Join(s.GalleryRoot,name+".png")
				if err:=os.WriteFile(p,b,0600);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,p,library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("fixture land",err)}
				return res.Image.ID
			}
			dup:=mode=="duplicate home"||mode=="same home"||mode=="duplicate no home"
			var id int64
			oldHome:="";oldOrder:=2
			wantLabel:="pool"
			if dup{
				id=land("held",data)
				if mode=="duplicate home"{oldHome="home"}
				if mode=="same home"{oldHome="POOL";wantLabel="POOL"}
				if oldHome!=""{
					if err:=gallery.SetHomeCollection(g.DB,id,oldHome,&oldOrder);err!=nil{t.Fatal(err)}
					if err:=gallery.AddCollectionMembership(g.DB,id,"other",nil);err!=nil{t.Fatal(err)}
				}
			}
			if mode=="existing label"{
				other:=land("other",pngBytes(2))
				if err:=gallery.AddCollectionMembership(g.DB,other,"POOL",&oldOrder);err!=nil{t.Fatal(err)}
				wantLabel="POOL"
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
			attempt:=filepath.Join(dir,"collection-attempt.json")
			result:=filepath.Join(dir,"collection-result.json")
			if dup{
				if err:=a.beginLanding(ctx,grant,g);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,filepath.Join(s.GalleryRoot,finalName(grant,f)),library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||!res.Duplicate||res.Image.ID!=id{t.Fatal("held duplicate",err)}
			}else{
				err:=a.landNewPrepared(ctx,grant,g)
				if mode=="missing order"{
					if err==nil{t.Fatal("invented missing collection order")}
					if _,err:=os.Stat(filepath.Join(dir,"landing-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected landing attempt",err)}
					return
				}
				if err!=nil{t.Fatal(err)}
			}
			mapped,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(mapped)!=1{t.Fatal("mapped",err)}
			if dup&&mapped[0].ImageID!=id{t.Fatal("changed duplicate identity")}
			id=mapped[0].ImageID
			if mode!="no relationship"{
				if err:=a.applyRelationships(ctx,grant,g);err!=nil{t.Fatal(err)}
			}
			// Collection is not yet applied by mapped metadata or relationships.
			before,err:=inspectCollection(ctx,g,id,claim.Collection)
			if err!=nil||before.State.Home!=oldHome{t.Fatal("prior state",err)}
			if mode=="write failure"{
				if _,err:=g.DB.Write.Exec(`CREATE TRIGGER fixture_refuse_collection BEFORE INSERT ON image_collections BEGIN SELECT RAISE(ABORT,'fixture failure'); END`);err!=nil{t.Fatal(err)}
			}
			if mode=="revoked after attempt"{
				a.authority=func(ctx context.Context,q workerproto.StageAllocateRequest)(Scope,error){
					if _,err:=os.Stat(attempt);err==nil{return Scope{},ErrStageRefused}
					return auth(ctx,q)
				}
			}
			err=a.applyCollections(ctx,grant,g)
			if mode=="no relationship"{
				if err==nil{t.Fatal("missing relationship accepted")}
				if _,err:=os.Stat(attempt);!os.IsNotExist(err){t.Fatal("unexpected collection attempt",err)}
				return
			}
			failed:=mode=="write failure"||mode=="revoked after attempt"
			if failed{
				if err==nil{t.Fatal("failed or revoked collection succeeded")}
				a.authority=auth
				if mode=="write failure"{
					if _,err:=g.DB.Write.Exec(`DROP TRIGGER fixture_refuse_collection`);err!=nil{t.Fatal(err)}
				}
				if _,err:=os.Stat(result);!os.IsNotExist(err){t.Fatal("unexpected result",err)}
			}else if err!=nil{t.Fatal(err)}
			if _,err:=os.Stat(attempt);err!=nil{t.Fatal("attempt missing",err)}
			after,err:=inspectCollection(ctx,g,id,claim.Collection)
			if err!=nil{t.Fatal(err)}
			wantHome:=wantLabel
			wantOrder:=int64(7)
			if mode=="duplicate home"{wantHome="home";wantOrder=2}
			if failed{
				if after.State.Home!=""||len(after.State.Members)!=0{t.Fatal("mutation on failed attempt")}
			}else{
				if after.State.Home!=wantHome||after.State.Position==nil||*after.State.Position!=wantOrder{t.Fatalf("home %+v want %s/%d",after.State,wantHome,wantOrder)}
				wantCount:=1
				if mode=="duplicate home"{wantCount=3}
				if mode=="same home"{wantCount=2}
				if len(after.State.Members)!=wantCount{t.Fatal("membership count",after.State.Members)}
				var n int
				if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_collections WHERE image_id=? AND name=? AND position=7`,id,wantLabel).Scan(&n);err!=nil||n!=1{t.Fatal("ordered collection missing",err)}
				if oldHome!=""{
					if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_collections WHERE image_id=? AND name='other' AND position IS NULL`,id).Scan(&n);err!=nil||n!=1{t.Fatal("other membership lost",err)}
				}
			}
			// The old terminal/disposal diagnostics cannot consume this phase.
			if _,err:=a.finishNewReceipt(ctx,grant,g);err==nil{t.Fatal("premature terminal receipt")}
			if dup{
				if _,err:=a.disposeHeldDuplicate(ctx,grant,g);err==nil{t.Fatal("premature duplicate disposal")}
				if _,err:=os.Stat(filepath.Join(s.GalleryRoot,finalName(grant,f)));err!=nil{t.Fatal("managed bytes lost",err)}
				if _,err:=os.Stat(filepath.Join(s.GalleryRoot,"held.png"));err!=nil{t.Fatal("held canonical lost",err)}
			}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
			if err:=a.applyCollections(ctx,grant,g);err==nil{t.Fatal("replayed collection attempt")}
			switch mode{
			case "lost result":
				if err:=os.Remove(result);err!=nil{t.Fatal(err)}
			case "changed position":
				if _,err:=g.DB.Write.Exec(`UPDATE image_collections SET position=8 WHERE image_id=?`,id);err!=nil{t.Fatal(err)}
			case "changed home":
				if _,err:=g.DB.Write.Exec(`UPDATE images SET series='wrong' WHERE id=?`,id);err!=nil{t.Fatal(err)}
			case "orphan result":
				if err:=os.Remove(attempt);err!=nil{t.Fatal(err)}
			case "damaged result":
				if err:=os.WriteFile(result,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			}
			err=a.observeCollections(ctx,grant,g)
			bad:=failed||mode=="lost result"||mode=="changed position"||mode=="changed home"||mode=="orphan result"||mode=="damaged result"
			if (err!=nil)!=bad{t.Fatal("unexpected reconciliation",err)}
		})
	}
}
