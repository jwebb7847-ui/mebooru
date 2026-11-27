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
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)
func TestComposedHeldReceipt(t *testing.T){
	for _,mode:=range []string{"plain","DB reopen","nested canonical","operator alias","collection","parent","missing relationship","missing collection","nonempty extras","no authority","changed retained before unlink","changed managed row","revoked before unlink","revoked after unlink","DB delete failure","changed canonical","same bytes replacement","changed operator alias","changed rating","changed note","changed source","changed collection","changed relationship","old alias returns","changed job","lost composition","damaged receipt","receipt revoked","extra retained alias"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,authority,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",
				Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{Original:"https://example.invalid/upstream"}}
			withCollection:=mode=="collection"||mode=="changed collection"||mode=="missing collection"
			withParent:=mode=="parent"||mode=="changed relationship"
			if withCollection{row.Provenance.Collection="pool";row.Provenance.CollectionOrder=7}
			if withParent{row.Provenance.ParentURL="https://example.invalid/parent"}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			g,err:=library.Open(cfg);if err!=nil{t.Fatal(err)}
			defer func(){g.Close()}()
			old:=filepath.Join(s.GalleryRoot,"existing.png")
			if mode=="nested canonical"{old=filepath.Join(s.GalleryRoot,"nested","existing.png");if err:=os.Mkdir(filepath.Dir(old),0700);err!=nil{t.Fatal(err)}}
			if err:=os.WriteFile(old,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			res,err:=g.LandInPlace(ctx,old,library.LandSpec{Origin:"fixture"})
			if err!=nil||res.Duplicate||res.Image==nil{t.Fatal("original",err)}
			id:=res.Image.ID
			if _,_,err:=gallery.MergeSource(g.DB,g.TagSvc,id,"other","7","https://example.invalid/7","","",gallery.PostFile{},[]string{"rating:explicit","prior"},gallery.Withheld{});err!=nil{t.Fatal(err)}
			if _,err:=g.DB.Write.Exec(`UPDATE images SET note='operator note' WHERE id=?`,id);err!=nil{t.Fatal(err)}
			oldOrder:=3
			if err:=gallery.SetHomeCollection(g.DB,id,"original-home",&oldOrder);err!=nil{t.Fatal(err)}
			var parent int64
			if withParent{
				var b bytes.Buffer
				if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,17,17)));err!=nil{t.Fatal(err)}
				res,err:=g.Land(ctx,s.GalleryRoot,"parent.png",library.FromReader(bytes.NewReader(b.Bytes())),library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("parent",err)}
				parent=res.Image.ID
				if _,_,err:=gallery.MergeSource(g.DB,g.TagSvc,parent,"other","8",row.Provenance.ParentURL,"","",gallery.PostFile{},[]string{"parent"},gallery.Withheld{});err!=nil{t.Fatal(err)}
			}
			operator:=filepath.Join(s.GalleryRoot,"operator.png")
			withAlias:=mode=="operator alias"||mode=="changed operator alias"
			if withAlias{
				if err:=os.WriteFile(operator,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,operator,library.LandSpec{Origin:"fixture"})
				if err!=nil||!res.Duplicate{t.Fatal("operator alias",err)}
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			if err:=a.beginLanding(ctx,grant,g);err!=nil{t.Fatal(err)}
			alias:=filepath.Join(s.GalleryRoot,finalName(grant,f))
			res,err=g.LandInPlace(ctx,alias,library.LandSpec{Origin:"fixture"})
			if err!=nil||!res.Duplicate||res.Image.ID!=id{t.Fatal("duplicate",err)}
			mapped,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(mapped)!=1||mapped[0].Rating!="explicit"{t.Fatal("metadata",err)}
			if mode!="missing relationship"{if err:=a.applyRelationships(ctx,grant,g);err!=nil{t.Fatal(err)}}
			if withCollection&&mode!="missing collection"{if err:=a.applyCollections(ctx,grant,g);err!=nil{t.Fatal(err)}}
			dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
			composition:=filepath.Join(dir,"held-composition.json");attempt:=filepath.Join(dir,"duplicate-attempt.json");receipt:=filepath.Join(dir,"held-receipt.json")
			if mode=="DB delete failure"{if _,err:=g.DB.Write.Exec(`CREATE TRIGGER refuse_alias_delete BEFORE DELETE ON image_paths BEGIN SELECT RAISE(ABORT,'fixture'); END`);err!=nil{t.Fatal(err)}}
			job:=contract.JobExtras{}
			if mode=="nonempty extras"{job.Tags=[]string{"unapplied"}}
			injected:=false
			extras:=extrasAuthority(func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){
				if mode=="changed retained before unlink"||mode=="changed managed row"||mode=="revoked before unlink"{
					if _,err:=os.Stat(attempt);err==nil&&!injected{
						injected=true
						if mode=="revoked before unlink"{return job,ErrStageRefused}
						if mode=="changed managed row"{
							if _,err:=g.DB.Write.Exec(`UPDATE image_paths SET id=id+10000 WHERE path=?`,alias);err!=nil{t.Fatal(err)}
						}else if _,err:=g.DB.Write.Exec(`UPDATE images SET note='changed before unlink' WHERE id=?`,id);err!=nil{t.Fatal(err)}
					}
				}
				if mode=="revoked after unlink"{
					if _,err:=os.Stat(alias);os.IsNotExist(err)&&!injected{injected=true;return job,ErrStageRefused}
				}
				if mode=="receipt revoked"{
					if _,err:=os.Stat(receipt);err==nil&&!injected{injected=true;return job,ErrStageRefused}
				}
				return job,nil
			})
			if mode=="no authority"{extras=nil}
			if _,err:=a.finishHeldReceipt(ctx,grant,g,extras);err==nil{t.Fatal("receipt before disposal")}
			got,err:=a.disposeComposedHeld(ctx,grant,g,extras)
			pre:=mode=="missing relationship"||mode=="missing collection"||mode=="nonempty extras"||mode=="no authority"
			partial:=mode=="changed retained before unlink"||mode=="changed managed row"||mode=="revoked before unlink"||mode=="revoked after unlink"||mode=="DB delete failure"
			if (err!=nil)!=(pre||partial){t.Fatal("unexpected disposal",got,err)}
			checkBytes:=func(p string,want []byte){t.Helper();b,err:=os.ReadFile(p);if err!=nil||!bytes.Equal(b,want){t.Fatal("bytes",p,err)}}
			count:=func(q string,args ...any)int{t.Helper();var n int;if err:=g.DB.Read.QueryRow(q,args...).Scan(&n);err!=nil{t.Fatal(err)};return n}
			checkBytes(old,buf.Bytes())
			if withAlias{checkBytes(operator,buf.Bytes())}
			checkBytes(filepath.Join(dir,"media",f.RelativeName),buf.Bytes())
			checkBytes(filepath.Join(stage,grant.Request.OperationID,"sealed","media",f.RelativeName),buf.Bytes())
			if pre{
				if _,err:=os.Stat(composition);!os.IsNotExist(err){t.Fatal("premature composition",err)}
				checkBytes(alias,buf.Bytes())
				return
			}
			if partial{
				if _,err:=a.finishHeldReceipt(ctx,grant,g,extras);err==nil{t.Fatal("partial receipt")}
				if _,err:=a.disposeComposedHeld(ctx,grant,g,extras);err==nil{t.Fatal("replay")}
				if mode=="revoked after unlink"||mode=="DB delete failure"{
					if _,err:=os.Stat(alias);!os.IsNotExist(err){t.Fatal("unlink boundary",err)}
				}else{checkBytes(alias,buf.Bytes())}
				if count(`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND path=?`,id,alias)!=1{t.Fatal("alias row unexpectedly deleted")}
				return
			}
			if got.ImageID!=id||count(`SELECT COUNT(*) FROM image_paths WHERE path=?`,alias)!=0{t.Fatal("wrong disposition")}
			if _,err:=os.Stat(alias);!os.IsNotExist(err){t.Fatal("alias retained",err)}
			if count(`SELECT COUNT(*) FROM images WHERE id=? AND note='operator note' AND series='original-home' AND series_order=3`,id)!=1{t.Fatal("operator state changed")}
			if withCollection&&count(`SELECT COUNT(*) FROM image_collections WHERE image_id=? AND name='pool' AND position=7`,id)!=1{t.Fatal("collection missing")}
			if withParent&&count(`SELECT COUNT(*) FROM derivative_edges WHERE derivative_image_id=? AND source_image_id=?`,id,parent)!=1{t.Fatal("relationship missing")}
			stored,err:=a.finishHeldReceipt(ctx,grant,g,extras)
			if mode=="receipt revoked"{
				if err==nil{t.Fatal("acknowledged revoked receipt")}
				if _,err:=os.Stat(receipt);err!=nil{t.Fatal("receipt not written",err)}
				stored,err=a.observeHeldReceipt(ctx,grant,g,extras)
			}
			if err!=nil||stored.Kind!=heldReceiptKind||stored.Held!=got{t.Fatal("held receipt",stored,err)}
			if _,err:=a.finishHeldReceipt(ctx,grant,g,extras);err==nil{t.Fatal("receipt overwritten")}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
			if mode=="DB reopen"{g.Close();g,err=library.Open(cfg);if err!=nil{t.Fatal(err)}}
			switch mode{
			case "changed canonical":if err:=os.WriteFile(old,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "same bytes replacement":
				if err:=os.Rename(old,old+".retained");err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(old,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			case "changed operator alias":if err:=os.WriteFile(operator,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "changed rating":
				if _,err:=g.DB.Write.Exec(`DELETE FROM image_tags WHERE image_id=? AND tag_id IN (SELECT t.id FROM tags t JOIN tag_categories c ON c.id=t.category_id WHERE c.name='rating')`,id);err!=nil{t.Fatal(err)}
			case "changed note":if _,err:=g.DB.Write.Exec(`UPDATE images SET note='changed' WHERE id=?`,id);err!=nil{t.Fatal(err)}
			case "changed source":if _,err:=g.DB.Write.Exec(`UPDATE image_sources SET commentary='changed' WHERE image_id=? AND site='other'`,id);err!=nil{t.Fatal(err)}
			case "changed collection":if _,err:=g.DB.Write.Exec(`UPDATE image_collections SET position=8 WHERE image_id=? AND name='pool'`,id);err!=nil{t.Fatal(err)}
			case "changed relationship":if _,err:=g.DB.Write.Exec(`DELETE FROM derivative_edges WHERE derivative_image_id=? AND source_image_id=?`,id,parent);err!=nil{t.Fatal(err)}
			case "old alias returns":if err:=os.WriteFile(alias,[]byte("operator"),0600);err!=nil{t.Fatal(err)}
			case "extra retained alias":
				p:=filepath.Join(s.GalleryRoot,"new-operator.png")
				if err:=os.WriteFile(p,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				if res,err:=g.LandInPlace(ctx,p,library.LandSpec{Origin:"fixture"});err!=nil||!res.Duplicate{t.Fatal("new operator alias",err)}
			case "changed job":job.Commentary="unapplied"
			case "lost composition":if err:=os.Remove(composition);err!=nil{t.Fatal(err)}
			case "damaged receipt":if err:=os.WriteFile(receipt,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			}
			recovered,err:=a.observeHeldReceipt(ctx,grant,g,extras)
			bad:=strings.HasPrefix(mode,"changed")||mode=="same bytes replacement"||mode=="old alias returns"||mode=="lost composition"||mode=="damaged receipt"||mode=="extra retained alias"
			if (err!=nil)!=bad||(!bad&&recovered!=stored){t.Fatal("unexpected recovery",err)}
			if _,err:=a.disposeComposedHeld(ctx,grant,g,extras);err==nil{t.Fatal("disposal replay")}
			if mode=="old alias returns"{checkBytes(alias,[]byte("operator"))}
			checkBytes(filepath.Join(dir,"media",f.RelativeName),buf.Bytes())
			checkBytes(filepath.Join(stage,grant.Request.OperationID,"sealed","media",f.RelativeName),buf.Bytes())
		})
	}
}
