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
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/models"
)
func TestNewOnlyReceiptThroughActualIngest(t *testing.T){
	for _,mode:=range []string{"complete","unrated","existing bytes","parent claim","missing provenance","legacy attempt","missing metadata result","changed row","changed file","damaged receipt","extra source tag","stale source tag","unexpected source claim","wrong origin","removed derived ownership"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,auth,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){_ = a.Close()}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"},
				Provenance:&acquisitionplan.ApprovedProvenance{Original:"https://example.invalid/upstream"}}
			if mode=="unrated"{row.Rating="";row.Tags=[]string{"fixture"}}
			if mode=="parent claim"{row.Provenance.ParentURL="https://derpibooru.org/images/40"}
			if mode=="missing provenance"{row.Provenance=nil}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			g,err:=library.Open(config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")})
			if err!=nil{t.Fatal(err)}
			defer g.Close()
			if mode=="existing bytes"{
				old:=filepath.Join(s.GalleryRoot,"existing.png")
				if err:=os.WriteFile(old,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				if _,err:=g.LandInPlace(ctx,old,library.LandSpec{Origin:"fixture"});err!=nil{t.Fatal(err)}
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			path:=filepath.Join(s.GalleryRoot,finalName(grant,f))
			p:=filepath.Join(stage,grant.Request.OperationID,"publication")
			if mode=="legacy attempt"{
				if err:=a.beginLanding(ctx,grant,g);err!=nil{t.Fatal(err)}
				if _,err:=g.LandInPlace(ctx,path,library.LandSpec{Origin:"fixture"});err!=nil{t.Fatal(err)}
			}else{
				err:=a.landNewPrepared(ctx,grant,g)
				if mode=="existing bytes"||mode=="parent claim"||mode=="missing provenance"{
					if err==nil{t.Fatal("unsupported new-only scope landed")}
					if _,err:=os.Stat(filepath.Join(p,"landing-attempt.json"));!os.IsNotExist(err){t.Fatal("unsupported scope wrote attempt",err)}
					return
				}
				if err!=nil{t.Fatal(err)}
				if err:=a.landNewPrepared(ctx,grant,g);err==nil{t.Fatal("new landing replay")}
			}
			if _,err:=a.finishNewReceipt(ctx,grant,g);err==nil{t.Fatal("receipt before metadata")}
			v,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(v)!=1||v[0].Rating!=row.Rating{t.Fatalf("metadata %+v %v",v,err)}
			if mode=="legacy attempt"{
				if _,err:=a.finishNewReceipt(ctx,grant,g);err==nil{t.Fatal("legacy observation mistaken for new-only receipt")}
				return
			}
			if mode=="missing metadata result"{
				if err:=os.Remove(filepath.Join(p,"metadata-result.json"));err!=nil{t.Fatal(err)}
				if _,err:=a.finishNewReceipt(ctx,grant,g);err==nil{t.Fatal("uncertain metadata received receipt")}
				return
			}
			var derived int
			if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_tag_sources WHERE image_id=? AND source=?`,v[0].ImageID,models.TagSourceMonbooru).Scan(&derived);err!=nil||derived==0{t.Fatal("fixture did not exercise library-derived metadata",err)}
			receipt,err:=a.finishNewReceipt(ctx,grant,g)
			if err!=nil||receipt.Kind!=newReceiptKind||len(receipt.Rows)!=1||receipt.Rows[0].ImageID!=v[0].ImageID{t.Fatalf("receipt %+v %v",receipt,err)}
			if _,err:=a.finishNewReceipt(ctx,grant,g);err==nil{t.Fatal("receipt overwritten")}
			naming:=func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:f.RelativeName,Source:row.SourceLabel,PostID:row.PostID},nil}
			extras:=func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){return contract.JobExtras{},nil}
			if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("legacy receipt accepted as composed terminal witness")}
			if err:=a.applyRelationships(ctx,grant,g);err==nil{t.Fatal("relationship attempt after terminal receipt")}
			if _,err:=os.Stat(filepath.Join(p,"relationship-attempt.json"));!os.IsNotExist(err){t.Fatal("post-terminal mutation",err)}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth)
			if err!=nil{t.Fatal(err)}
			switch mode{
			case "changed row":
				if _,err:=g.DB.Write.Exec(`UPDATE images SET file_size=file_size+1 WHERE id=?`,v[0].ImageID);err!=nil{t.Fatal(err)}
			case "changed file":
				if err:=os.WriteFile(path,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "damaged receipt":
				if err:=os.WriteFile(filepath.Join(p,"new-receipt.json"),[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "extra source tag":
				if _,err:=g.DB.Write.Exec(`INSERT INTO image_tag_sources(image_id,tag_id,source) SELECT image_id,tag_id,'extra' FROM image_tag_sources WHERE image_id=? LIMIT 1`,v[0].ImageID);err!=nil{t.Fatal(err)}
				// Original attribution remains unchanged: an extra owner alone
				// must invalidate the isolated new-only observation.
			case "stale source tag":
				if _,err:=g.DB.Write.Exec(`UPDATE image_tags SET stale=1 WHERE image_id=?`,v[0].ImageID);err!=nil{t.Fatal(err)}
			case "unexpected source claim":
				if _,err:=g.DB.Write.Exec(`UPDATE image_sources SET commentary='unexpected' WHERE image_id=?`,v[0].ImageID);err!=nil{t.Fatal(err)}
			case "wrong origin":
				if _,err:=g.DB.Write.Exec(`UPDATE images SET origin='other' WHERE id=?`,v[0].ImageID);err!=nil{t.Fatal(err)}
			case "removed derived ownership":
				if _,err:=g.DB.Write.Exec(`DELETE FROM image_tag_sources WHERE image_id=? AND source=?`,v[0].ImageID,models.TagSourceMonbooru);err!=nil{t.Fatal(err)}
			}
			got,err:=a.observeNewReceipt(ctx,grant,g)
			if mode=="complete"||mode=="unrated"{
				if err!=nil||got.Allocation!=receipt.Allocation||!slices.Equal(got.Rows,receipt.Rows){t.Fatalf("recovered %+v %v",got,err)}
				if _,err:=os.Stat(filepath.Join(stage,grant.Request.OperationID));err!=nil{t.Fatal("receipt reclaimed evidence",err)}
			}else if err==nil||got.Version!=0{t.Fatal("changed receipt accepted")}
		})
	}
}
