package acquisitionstage

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

func TestPlannedNewPublication(t *testing.T){
	for _,mode:=range []string{"plain","template","collision","nested","unbound","bad PNG","wrong extension","parent","collection","existing image","wrong naming","future ID","future date","missing directory","custom via","commentary","rating alias","no authority","changed naming","changed job","changed collision","changed directory","forged plan","revoked after intent","changed after intent"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			data:=buf.Bytes();ext:="png"
			if mode=="bad PNG"{data=[]byte("not a PNG")}
			if mode=="wrong extension"{ext="jpg"}
			a,stage,s,grant,_,f:=publicationFixtureData(t,ext,data)
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42."+ext,
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}}
			if mode=="parent"{row.Provenance.ParentURL="https://derpibooru.org/images/41"}
			if mode=="collection"{row.Provenance.Collection="pool";row.Provenance.CollectionOrder=1}
			if mode!="unbound"{
				body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
				if err!=nil{t.Fatal(err)}
				if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			g,err:=library.Open(cfg)
			if err!=nil{t.Fatal(err)}
			defer g.Close()
			if mode=="existing image"{
				res,err:=g.Land(ctx,s.GalleryRoot,"existing.png",library.FromReader(bytes.NewReader(data)),library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("existing fixture",err)}
			}
			req:=gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:f.RelativeName,Source:row.SourceLabel,PostID:row.PostID}
			job:=contract.JobExtras{Tags:[]string{"client tag"}}
			switch mode{
			case "template":req.DefaultName="{source}-{post_id}-{hash:8}"
			case "collision":
				if err:=os.WriteFile(filepath.Join(s.GalleryRoot,f.RelativeName),[]byte("operator"),0600);err!=nil{t.Fatal(err)}
			case "nested","changed directory":
				req.RequestFolder="nested"
				if err:=os.Mkdir(filepath.Join(s.GalleryRoot,"nested"),0700);err!=nil{t.Fatal(err)}
			case "wrong naming":req.ReceivedName="foreign.png"
			case "future ID":req.DefaultName="{id}"
			case "future date":req.DefaultName="{date}"
			case "missing directory":req.RequestFolder="missing"
			case "custom via":job.Via="foreign"
			case "commentary":job.Commentary="not supported"
			case "rating alias":
				cat,bare,err:=gallery.ResolveCategoryTag(g.DB,"rating:explicit")
				if err!=nil{t.Fatal(err)}
				tag,err:=g.TagSvc.GetOrCreateTag(bare,cat)
				if err!=nil{t.Fatal(err)}
				cat,bare,err=gallery.ResolveCategoryTag(g.DB,"client_tag")
				if err!=nil{t.Fatal(err)}
				if _,err:=g.TagSvc.CreateAlias(bare,cat,tag.ID);err!=nil{t.Fatal(err)}
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			intent:=filepath.Join(stage,grant.Request.OperationID,"publication","attempt")
			diagnostic:=filepath.Join(s.GalleryRoot,finalName(grant,f))
			injected:=false
			naming:=namingAuthority(func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){
				if _,err:=os.Stat(intent);err==nil{
					if mode=="revoked after intent"{injected=true;return req,ErrStageRefused}
					if mode=="changed after intent"{injected=true;changed:=req;changed.DefaultName="late";return changed,nil}
				}
				return req,nil
			})
			extras:=extrasAuthority(func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){return job,nil})
			if mode=="no authority"{extras=nil}
			count:=func() [2]int {
				t.Helper();var out [2]int
				if err:=g.DB.Read.QueryRow("SELECT COUNT(*) FROM images").Scan(&out[0]);err!=nil{t.Fatal(err)}
				if err:=g.DB.Read.QueryRow("SELECT COUNT(*) FROM image_paths").Scan(&out[1]);err!=nil{t.Fatal(err)}
				return out
			}
			before:=count()
			plan,err:=a.planNewPublication(ctx,grant,g,naming,extras)
			if _,err:=os.Stat(intent);!os.IsNotExist(err){t.Fatal("planning wrote intent",err)}
			if _,err:=os.Stat(diagnostic);!os.IsNotExist(err){t.Fatal("planning published media",err)}
			if count()!=before{t.Fatal("planning mutated gallery rows")}
			switch mode{
			case "unbound","bad PNG","wrong extension","parent","collection","existing image","wrong naming","future ID","future date","missing directory","custom via","commentary","rating alias","no authority":
				if err==nil{t.Fatal("unsupported plan accepted")}
				if _,err:=a.publishPlannedNew(ctx,grant,g,naming,extras,plan);err==nil{t.Fatal("unsupported publication accepted")}
				if _,err:=os.Stat(intent);!os.IsNotExist(err){t.Fatal("unsupported case wrote intent",err)}
				if _,err:=os.Stat(diagnostic);!os.IsNotExist(err){t.Fatal("unsupported case published media",err)}
				if count()!=before{t.Fatal("unsupported case mutated gallery")}
				return
			}
			if err!=nil{t.Fatal("plan",err)}
			switch mode{
			case "changed naming":req.DefaultName="changed"
			case "changed job":job.Tags=[]string{"changed"}
			case "changed collision":
				if err:=os.WriteFile(plan.Preview.InitialPath,[]byte("late operator"),0600);err!=nil{t.Fatal(err)}
			case "changed directory":
				old:=filepath.Join(s.GalleryRoot,"nested")
				if err:=os.Rename(old,filepath.Join(s.GalleryRoot,"old"));err!=nil{t.Fatal(err)}
				if err:=os.Mkdir(old,0700);err!=nil{t.Fatal(err)}
			case "forged plan":plan.FileIdentity="wrong"
			}
			places,err:=a.publishPlannedNew(ctx,grant,g,naming,extras,plan)
			switch mode{
			case "changed naming","changed job","changed collision","changed directory","forged plan","revoked after intent","changed after intent":
				if err==nil{t.Fatal("changed plan published")}
				_,statErr:=os.Stat(intent)
				partial:=mode=="revoked after intent"||mode=="changed after intent"
				if partial{if statErr!=nil||!injected{t.Fatal("post-intent injection not reached",statErr)}}else if !os.IsNotExist(statErr){t.Fatal("unexpected intent",statErr)}
				if _,err:=os.Stat(diagnostic);!os.IsNotExist(err){t.Fatal("refused publication linked",err)}
				if count()!=before{t.Fatal("refused publication mutated gallery")}
				if partial{if _,err:=a.publishPlannedNew(ctx,grant,g,naming,extras,plan);err==nil{t.Fatal("uncertain publication replayed")}}
				return
			}
			if err!=nil||len(places)!=1||places[0].State!="placed"{t.Fatal("publish",places,err)}
			if count()!=before{t.Fatal("publication wrote DB rows")}
			if _,err:=a.publishPlannedNew(ctx,grant,g,naming,extras,plan);err==nil{t.Fatal("publication replayed")}
			if err:=a.landNewPrepared(ctx,grant,g);err!=nil{t.Fatal("land",err)}
			mapped,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(mapped)!=1{t.Fatal("metadata",err)}
			if err:=a.applyRelationships(ctx,grant,g);err!=nil{t.Fatal(err)}
			preview,err:=gallery.PreviewReceivedNaming(ctx,g.DB,g.Boundary(),mapped[0].ImageID,req)
			if err!=nil||!reflect.DeepEqual(preview,plan.Preview){t.Fatal("prepublication and actual landed naming diverged",preview,plan.Preview,err)}
			if err:=a.applyNaming(ctx,grant,g,naming);err!=nil{t.Fatal(err)}
			if err:=a.applyClientExtras(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}
			if _,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}
			if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}
			if got,err:=os.ReadFile(preview.FinalPath);err!=nil||!bytes.Equal(got,data){t.Fatal("canonical bytes",err)}
			if mode=="collision"{if got,err:=os.ReadFile(filepath.Join(s.GalleryRoot,f.RelativeName));err!=nil||string(got)!="operator"{t.Fatal("operator bytes changed",err)}}
		})
	}
}
