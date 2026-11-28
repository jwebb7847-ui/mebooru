package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
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
	for _,mode:=range []string{"plain","template","collision","nested","unbound","bad PNG","wrong extension","parent","collection","existing image","wrong naming","future ID","future date","missing directory","custom via","commentary","rating alias","no authority","changed naming","changed job","changed collision","changed directory","forged plan","revoked after intent","changed after intent","DB reopen","changed after publish","changed job after publish","changed collision after publish","revoked after landing intent","damaged attempt","changed after landing","changed job after naming","changed client after landing","shared source tag","legacy marker substitution","lost attempt after receipt","full closure","JPEG","JPEG extension","animated GIF","WebP","bad JPEG","mismatched GIF","unsupported video","media facts changed","media facts changed after receipt","forged media facts","measured naming","collection existing label","collection missing order","collection changed before publish","collection changed after publish","collection changed before apply","collection missing result","collection changed after receipt","collection JPEG"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			data:=buf.Bytes();ext:="png"
			if mode=="bad PNG"{data=[]byte("not a PNG")}
			if mode=="wrong extension"{ext="jpg"}
			switch mode{
			case "JPEG","JPEG extension","media facts changed","media facts changed after receipt","forged media facts","measured naming","collection JPEG":
				var out bytes.Buffer
				if err:=jpeg.Encode(&out,image.NewRGBA(image.Rect(0,0,16,16)),nil);err!=nil{t.Fatal(err)}
				data=out.Bytes();ext="jpg"
				if mode=="JPEG extension"{ext="jpeg"}
			case "animated GIF":
				palette:=color.Palette{color.Black,color.White}
				first:=image.NewPaletted(image.Rect(0,0,16,16),palette)
				second:=image.NewPaletted(image.Rect(0,0,16,16),palette)
				second.SetColorIndex(4,4,1)
				var out bytes.Buffer
				if err:=gif.EncodeAll(&out,&gif.GIF{Image:[]*image.Paletted{first,second},Delay:[]int{10,10}});err!=nil{t.Fatal(err)}
				data=out.Bytes();ext="gif"
			case "WebP":
				var err error
				data,err=base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvD8ADAAdQvEIVuf+BiOh/AAA=")
				if err!=nil{t.Fatal(err)}
				ext="webp"
			case "bad JPEG":data=[]byte("not a JPEG");ext="jpg"
			case "mismatched GIF":ext="gif"
			case "unsupported video":ext="mp4"
			}
			a,stage,s,grant,authority,f:=publicationFixtureData(t,ext,data)
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42."+ext,
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}}
			if mode=="parent"{row.Provenance.ParentURL="https://derpibooru.org/images/41"}
			withCollection:=strings.HasPrefix(mode,"collection")
			if withCollection{row.Provenance.Collection="pool";row.Provenance.CollectionOrder=7}
			if mode=="collection missing order"{row.Provenance.CollectionOrder=0}
			if mode!="unbound"{
				body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
				if err!=nil{t.Fatal(err)}
				if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			g,err:=library.Open(cfg)
			if err!=nil{t.Fatal(err)}
			defer func(){g.Close()}()
			if mode=="existing image"{
				res,err:=g.Land(ctx,s.GalleryRoot,"existing.png",library.FromReader(bytes.NewReader(data)),library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("existing fixture",err)}
			}
			var existingCollectionID int64
			var existingCollectionBefore collectionSnapshot
			addExistingCollection:=func(){
				t.Helper()
				var b bytes.Buffer
				if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,17,17)));err!=nil{t.Fatal(err)}
				res,err:=g.Land(ctx,s.GalleryRoot,"existing-collection.png",library.FromReader(bytes.NewReader(b.Bytes())),library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("collection fixture",err)}
				order:=2
				if err:=gallery.AddCollectionMembership(g.DB,res.Image.ID,"POOL",&order);err!=nil{t.Fatal(err)}
				existingCollectionID=res.Image.ID
				existingCollectionBefore,err=inspectCollection(ctx,g,existingCollectionID,"POOL")
				if err!=nil{t.Fatal("existing collection snapshot",err)}
			}
			assertExistingCollectionUnchanged:=func(){
				t.Helper()
				if mode!="collection existing label"{return}
				if existingCollectionID<=0{t.Fatal("missing existing collection fixture")}
				after,err:=inspectCollection(ctx,g,existingCollectionID,"POOL")
				if err!=nil||!reflect.DeepEqual(after,existingCollectionBefore){t.Fatal("existing collection membership changed",existingCollectionBefore,after,err)}
			}
			if mode=="collection existing label"{addExistingCollection()}
			req:=gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:f.RelativeName,Source:row.SourceLabel,PostID:row.PostID}
			job:=contract.JobExtras{Tags:[]string{"client tag"}}
			switch mode{
			case "shared source tag":job.Tags=append(job.Tags,"fixture")
			case "template":req.DefaultName="{source}-{post_id}-{hash:8}"
			case "measured naming":req.DefaultName="{type}-{w}x{h}-{md5}"
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
			landingPath:=filepath.Join(stage,grant.Request.OperationID,"publication","landing-attempt.json")
			injected:=false
			naming:=namingAuthority(func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){
				if _,err:=os.Stat(intent);err==nil{
					if mode=="revoked after intent"{injected=true;return req,ErrStageRefused}
					if mode=="changed after intent"{injected=true;changed:=req;changed.DefaultName="late";return changed,nil}
				}
				if mode=="revoked after landing intent"{
					if _,err:=os.Stat(landingPath);err==nil{injected=true;return req,ErrStageRefused}
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
			case "unbound","bad PNG","wrong extension","parent","collection missing order","existing image","wrong naming","future ID","future date","missing directory","custom via","commentary","rating alias","no authority","bad JPEG","mismatched GIF","unsupported video":
				if err==nil{t.Fatal("unsupported plan accepted")}
				if _,err:=a.publishPlannedNew(ctx,grant,g,naming,extras,plan);err==nil{t.Fatal("unsupported publication accepted")}
				if _,err:=os.Stat(intent);!os.IsNotExist(err){t.Fatal("unsupported case wrote intent",err)}
				if _,err:=os.Stat(diagnostic);!os.IsNotExist(err){t.Fatal("unsupported case published media",err)}
				if count()!=before{t.Fatal("unsupported case mutated gallery")}
				return
			}
			if err!=nil{t.Fatal("plan",err)}
			if !validPlannedImageFacts(f,plan.Media)||plan.Media.Width!=16||plan.Media.Height!=16{t.Fatal("missing measured media facts",plan.Media)}
			if withCollection{
				label:="pool";if mode=="collection existing label"{label="POOL"}
				if plan.Collection==nil||plan.Collection.Label!=label||plan.Collection.Order!=7{t.Fatal("wrong planned collection",plan.Collection)}
			}else if plan.Collection!=nil{t.Fatal("unexpected collection plan")}
			switch mode{
			case "changed naming":req.DefaultName="changed"
			case "changed job":job.Tags=[]string{"changed"}
			case "changed collision":
				if err:=os.WriteFile(plan.Preview.InitialPath,[]byte("late operator"),0600);err!=nil{t.Fatal(err)}
			case "changed directory":
				old:=filepath.Join(s.GalleryRoot,"nested")
				if err:=os.Rename(old,filepath.Join(s.GalleryRoot,"old"));err!=nil{t.Fatal(err)}
				if err:=os.Mkdir(old,0700);err!=nil{t.Fatal(err)}
			case "collection changed before publish":addExistingCollection();before=count()
			case "forged plan":plan.FileIdentity="wrong"
			case "forged media facts":plan.Media.Width++
			}
			places,err:=a.publishPlannedNew(ctx,grant,g,naming,extras,plan)
			switch mode{
			case "collection changed before publish","changed naming","changed job","changed collision","changed directory","forged plan","forged media facts","revoked after intent","changed after intent":
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
			if err:=a.landNewPrepared(ctx,grant,g);err==nil{t.Fatal("diagnostic landing bypassed guarded plan")}
			if _,err:=os.Stat(landingPath);!os.IsNotExist(err){t.Fatal("diagnostic landing wrote intent",err)}
			if mode=="DB reopen"||mode=="full closure"{
				if err:=a.Close();err!=nil{t.Fatal(err)}
				g.Close()
				a,err=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
				g,err=library.Open(cfg);if err!=nil{t.Fatal(err)}
			}
			switch mode{
			case "collection changed after publish":addExistingCollection();before=count()
			case "changed after publish":req.DefaultName="changed"
			case "changed job after publish":job.Tags=[]string{"changed"}
			case "changed collision after publish":
				if err:=os.WriteFile(plan.Preview.InitialPath,[]byte("operator"),0600);err!=nil{t.Fatal(err)}
			case "damaged attempt":
				if err:=os.WriteFile(intent,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			}
			err=a.landPlannedNew(ctx,grant,g,naming,extras)
			switch mode{
			case "collection changed after publish","changed after publish","changed job after publish","changed collision after publish","revoked after landing intent","damaged attempt":
				if err==nil||count()!=before{t.Fatal("unsafe planned landing",err)}
				_,statErr:=os.Stat(landingPath)
				if mode=="revoked after landing intent"{if statErr!=nil||!injected{t.Fatal("landing boundary injection not reached",statErr)}}else if !os.IsNotExist(statErr){t.Fatal("unexpected landing intent",statErr)}
				if got,err:=os.ReadFile(diagnostic);err!=nil||!bytes.Equal(got,data){t.Fatal("published diagnostic lost",err)}
				return
			}
			if err!=nil{t.Fatal("planned land",err)}
			if mode=="legacy marker substitution"{
				if err:=os.WriteFile(intent,[]byte(grant.TransferID),0600);err!=nil{t.Fatal(err)}
				if _,err:=a.applyMappedMetadata(ctx,grant,g);err==nil{t.Fatal("legacy marker replaced plan")}
				if _,err:=os.Stat(filepath.Join(filepath.Dir(intent),"metadata-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected metadata attempt",err)}
				return
			}
			mapped,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(mapped)!=1{t.Fatal("metadata",err)}
			if err:=a.applyRelationships(ctx,grant,g);err!=nil{t.Fatal(err)}
			if withCollection{
				if mode=="collection changed before apply"{addExistingCollection()}
				if mode=="collection missing result"{
					if err:=a.applyNaming(ctx,grant,g,naming);err==nil{t.Fatal("naming without collection proof")}
					return
				}
				err:=a.applyCollections(ctx,grant,g)
				if mode=="collection changed before apply"{
					if err==nil{t.Fatal("changed collection accepted")}
					if _,err:=os.Stat(filepath.Join(filepath.Dir(intent),"collection-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected collection intent",err)}
					return
				}
				if err!=nil{t.Fatal("apply collection",err)}
				var label string;var position int
				if err:=g.DB.Read.QueryRow("SELECT name,position FROM image_collections WHERE image_id=?",mapped[0].ImageID).Scan(&label,&position);err!=nil||label!=plan.Collection.Label||position!=7{t.Fatal("collection membership",label,position,err)}
				assertExistingCollectionUnchanged()
			}
			if mode=="media facts changed"{
				if _,err:=g.DB.Write.Exec("UPDATE images SET width=17 WHERE id=?",mapped[0].ImageID);err!=nil{t.Fatal(err)}
				if err:=a.applyNaming(ctx,grant,g,naming);err==nil{t.Fatal("changed measured width accepted")}
				if _,err:=os.Stat(filepath.Join(filepath.Dir(intent),"naming-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected naming attempt",err)}
				return
			}
			if _,err:=a.finishNewReceipt(ctx,grant,g);err==nil{t.Fatal("narrow receipt bypassed guarded plan")}
			if mode=="changed after landing"{
				req.DefaultName="changed"
				if err:=a.applyNaming(ctx,grant,g,naming);err==nil{t.Fatal("changed naming accepted")}
				if _,err:=os.Stat(filepath.Join(filepath.Dir(intent),"naming-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected naming attempt",err)}
				return
			}
			preview,err:=gallery.PreviewReceivedNaming(ctx,g.DB,g.Boundary(),mapped[0].ImageID,req)
			if err!=nil||!reflect.DeepEqual(preview,plan.Preview){t.Fatal("prepublication and actual landed naming diverged",preview,plan.Preview,err)}
			if err:=a.applyNaming(ctx,grant,g,naming);err!=nil{t.Fatal(err)}
			if mode=="changed job after naming"{job.Tags=[]string{"changed"}}
			if mode=="changed client after landing"{
				cat,bare,err:=gallery.ResolveCategoryTag(g.DB,"client_tag")
				if err!=nil{t.Fatal(err)}
				if _,err:=g.TagSvc.GetOrCreateTag(bare,cat);err!=nil{t.Fatal(err)}
			}
			err=a.applyClientExtras(ctx,grant,g,naming,extras)
			if mode=="changed job after naming"||mode=="changed client after landing"{
				if err==nil{t.Fatal("unplanned client mutation accepted")}
				if _,err:=os.Stat(filepath.Join(filepath.Dir(intent),"extras-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected extras attempt",err)}
				return
			}
			if err!=nil{t.Fatal(err)}
			receipt,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);if err!=nil{t.Fatal(err)}
			if ext!="png"&&receipt.Kind!=guardedImageReceiptKind{t.Fatal("wrong receipt kind",receipt.Kind)}
			if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}
			if got,err:=os.ReadFile(preview.FinalPath);err!=nil||!bytes.Equal(got,data){t.Fatal("canonical bytes",err)}
			if mode=="collision"{if got,err:=os.ReadFile(filepath.Join(s.GalleryRoot,f.RelativeName));err!=nil||string(got)!="operator"{t.Fatal("operator bytes changed",err)}}
			if mode=="collection changed after receipt"{
				if _,err:=g.DB.Write.Exec("UPDATE image_collections SET position=8 WHERE image_id=?",mapped[0].ImageID);err!=nil{t.Fatal(err)}
				if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("changed collection accepted")}
				return
			}
			if mode=="media facts changed after receipt"{
				if _,err:=g.DB.Write.Exec("UPDATE images SET file_type='png' WHERE id=?",mapped[0].ImageID);err!=nil{t.Fatal(err)}
				if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("changed type accepted")}
				return
			}
			if mode=="lost attempt after receipt"{
				if err:=os.Remove(intent);err!=nil{t.Fatal(err)}
				if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("lost guarded marker accepted")}
				return
			}
			if mode=="full closure"||mode=="JPEG"||mode=="JPEG extension"||mode=="animated GIF"||mode=="WebP"||mode=="collection"||mode=="collection existing label"||mode=="collection JPEG"{
				auth:=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return nil}
				if _,err:=a.reclaimWorkspace(ctx,grant,g,naming,extras,auth);err!=nil{t.Fatal("reclaim",err)}
				if _,err:=a.releasePrepared(ctx,grant,g,naming,extras,auth);err!=nil{t.Fatal("release",err)}
				if _,err:=a.retireSealedMedia(ctx,grant,g,naming,extras,auth);err!=nil{t.Fatal("retire",err)}
				if _,err:=a.closeRetiredOperation(ctx,grant,g,naming,extras,auth);err!=nil{t.Fatal("close",err)}
				if _,err:=a.observeClosedOperation(ctx,grant,g,naming,extras,auth);err!=nil{t.Fatal("observe closed",err)}
				assertExistingCollectionUnchanged()
				if got,err:=os.ReadFile(preview.FinalPath);err!=nil||!bytes.Equal(got,data){t.Fatal("canonical bytes after closure",err)}
			}
		})
	}
}
