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
func TestComposedRestoredReceipt(t *testing.T){
	for _,mode:=range []string{"marked missing","unmarked missing","DB reopen","unrated prior","nested destination","collision","manual annotation","missing relationships",
		"changed prior note before metadata","changed prior collection before metadata","changed old row before metadata",
		"nonempty extras","no authority","old path requested","old path returns before move","changed state after intent","revoked after intent","revoked after link",
		"DB move failure","revoked after DB move","lost naming result","damaged naming result","changed canonical","same bytes replacement",
		"old path returns after receipt","changed old row","changed final row","changed note","changed rating","changed membership","changed source","changed job","changed naming","damaged receipt","receipt revoked"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,authority,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",
				Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			g,err:=library.Open(cfg);if err!=nil{t.Fatal(err)}
			defer func(){g.Close()}()
			old:=filepath.Join(s.GalleryRoot,"existing.png")
			if err:=os.WriteFile(old,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			res,err:=g.LandInPlace(ctx,old,library.LandSpec{Origin:"fixture"})
			if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("initial land",err)}
			id:=res.Image.ID
			priorTags:=[]string{"rating:explicit","prior"}
			priorRating,wantRating:="explicit","explicit"
			if mode=="unrated prior"{priorTags=[]string{"prior"};priorRating="";wantRating="general"}
			if _,_,err:=gallery.MergeSource(g.DB,g.TagSvc,id,"other","7","https://example.invalid/7","","",gallery.PostFile{},priorTags,gallery.Withheld{});err!=nil{t.Fatal(err)}
			if _,err:=g.DB.Write.Exec(`UPDATE images SET note='operator note',original_source='https://example.invalid/operator-original',is_favorited=1,is_inbox=0 WHERE id=?`,id);err!=nil{t.Fatal(err)}
			order:=3
			if err:=gallery.SetHomeCollection(g.DB,id,"original-home",&order);err!=nil{t.Fatal(err)}
			var oldPathID int64
			if err:=g.DB.Read.QueryRow(`SELECT id FROM image_paths WHERE path=?`,old).Scan(&oldPathID);err!=nil{t.Fatal(err)}
			if mode=="manual annotation"{
				if _,err:=g.DB.Write.Exec(`INSERT INTO image_annotations(image_id,x,y,w,h,body,manual) VALUES(?,1,2,3,4,'operator annotation',1)`,id);err!=nil{t.Fatal(err)}
			}
			if err:=os.Remove(old);err!=nil{t.Fatal(err)}
			marked:=mode!="unmarked missing"
			if marked{if _,err:=gallery.SetMissing(g.DB,id,true);err!=nil{t.Fatal(err)}}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			if err:=a.landRestoredPrepared(ctx,grant,g);err!=nil{t.Fatal(err)}
			if err:=a.landRestoredPrepared(ctx,grant,g);err==nil{t.Fatal("restoration replayed")}
			switch mode{
			case "changed prior note before metadata":if _,err:=g.DB.Write.Exec(`UPDATE images SET note='changed' WHERE id=?`,id);err!=nil{t.Fatal(err)}
			case "changed prior collection before metadata":if _,err:=g.DB.Write.Exec(`UPDATE image_collections SET position=9 WHERE image_id=?`,id);err!=nil{t.Fatal(err)}
			case "changed old row before metadata":if _,err:=g.DB.Write.Exec(`UPDATE image_paths SET id=id+10000 WHERE path=?`,old);err!=nil{t.Fatal(err)}
			}
			if mode=="changed prior note before metadata"||mode=="changed prior collection before metadata"||mode=="changed old row before metadata"{
				if _,err:=a.applyMappedMetadata(ctx,grant,g);err==nil{t.Fatal("changed pre-restoration invariant accepted")}
				if _,err:=os.Stat(filepath.Join(stage,grant.Request.OperationID,"publication","metadata-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected metadata attempt",err)}
				return
			}
			metadata,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(metadata)!=1||metadata[0].ImageID!=id||metadata[0].Rating!=wantRating{t.Fatal("metadata",err,metadata)}
			if mode!="missing relationships"{if err:=a.applyRelationships(ctx,grant,g);err!=nil{t.Fatal(err)}}
			var origin string
			if err:=g.DB.Read.QueryRow(`SELECT origin FROM images WHERE id=?`,id).Scan(&origin);err!=nil{t.Fatal(err)}
			dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
			attempt:=filepath.Join(dir,"naming-attempt.json");result:=filepath.Join(dir,"naming-result.json");receipt:=filepath.Join(dir,"restored-receipt.json")
			diag:=filepath.Join(s.GalleryRoot,finalName(grant,f))
			req:=gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:f.RelativeName,Source:row.SourceLabel,PostID:row.PostID,DefaultName:"{source}-{post_id}"}
			final:=filepath.Join(s.GalleryRoot,"derpibooru-42.png")
			if mode=="nested destination"{
				req.RequestFolder="nested"
				if err:=os.Mkdir(filepath.Join(s.GalleryRoot,"nested"),0700);err!=nil{t.Fatal(err)}
				final=filepath.Join(s.GalleryRoot,"nested","derpibooru-42.png")
			}
			if mode=="collision"{
				if err:=os.WriteFile(final,[]byte("operator"),0600);err!=nil{t.Fatal(err)}
				final=filepath.Join(s.GalleryRoot,"derpibooru-4201.png")
			}
			if mode=="old path requested"{req.DefaultName="existing"}
			if mode=="old path returns before move"{if err:=os.WriteFile(old,[]byte("operator"),0600);err!=nil{t.Fatal(err)}}
			if mode=="DB move failure"{
				if _,err:=g.DB.Write.Exec(`CREATE TRIGGER refuse_canonical_move BEFORE UPDATE OF canonical_path ON images BEGIN SELECT RAISE(ABORT,'fixture'); END`);err!=nil{t.Fatal(err)}
			}
			job:=contract.JobExtras{}
			if mode=="nonempty extras"{job.Tags=[]string{"unapplied"}}
			injected:=false
			naming:=func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return req,nil}
			extras:=extrasAuthority(func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){
				if mode=="changed state after intent"||mode=="revoked after intent"{
					if _,err:=os.Stat(attempt);err==nil&&!injected{
						injected=true
						if mode=="revoked after intent"{return job,ErrStageRefused}
						if _,err:=g.DB.Write.Exec(`UPDATE images SET note='changed after intent' WHERE id=?`,id);err!=nil{t.Fatal(err)}
					}
				}
				if mode=="revoked after link"{
					if _,err:=os.Stat(final);err==nil&&!injected{injected=true;return job,ErrStageRefused}
				}
				if mode=="revoked after DB move"{
					var p string
					if err:=g.DB.Read.QueryRow(`SELECT canonical_path FROM images WHERE id=?`,id).Scan(&p);err!=nil{t.Fatal(err)}
					if p==final&&!injected{injected=true;return job,ErrStageRefused}
				}
				if mode=="receipt revoked"{
					if _,err:=os.Stat(receipt);err==nil&&!injected{injected=true;return job,ErrStageRefused}
				}
				return job,nil
			})
			if mode=="no authority"{extras=nil}
			if _,err:=a.finishRestoredReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("receipt before naming")}
			if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("terminal witness before receipt")}
			if err:=a.applyNaming(ctx,grant,g,naming);err==nil{t.Fatal("new-only naming accepted restoration")}
			if _,err:=a.disposeHeldDuplicate(ctx,grant,g);err==nil{t.Fatal("restored canonical disposed")}
			err=a.applyRestoredNaming(ctx,grant,g,naming,extras)
			pre:=mode=="missing relationships"||mode=="nonempty extras"||mode=="no authority"||mode=="old path requested"||mode=="old path returns before move"
			partial:=mode=="changed state after intent"||mode=="revoked after intent"||mode=="revoked after link"||mode=="DB move failure"||mode=="revoked after DB move"
			if (err!=nil)!=(pre||partial){
				var actual string
				_ = g.DB.Read.QueryRow(`SELECT canonical_path FROM images WHERE id=?`,id).Scan(&actual)
				_,intentErr:=os.Stat(attempt);_,resultErr:=os.Stat(result)
				t.Fatalf("unexpected naming %v; canonical=%q; intent=%v; result=%v",err,actual,intentErr,resultErr)
			}
			checkBytes:=func(p string,want []byte){t.Helper();b,err:=os.ReadFile(p);if err!=nil||!bytes.Equal(b,want){t.Fatal("bytes",p,err)}}
			count:=func(q string,args ...any)int{t.Helper();var n int;if err:=g.DB.Read.QueryRow(q,args...).Scan(&n);err!=nil{t.Fatal(err)};return n}
			checkBytes(filepath.Join(dir,"media",f.RelativeName),buf.Bytes())
			checkBytes(filepath.Join(stage,grant.Request.OperationID,"sealed","media",f.RelativeName),buf.Bytes())
			if pre{
				if _,err:=os.Stat(attempt);!os.IsNotExist(err){t.Fatal("premature attempt",err)}
				checkBytes(diag,buf.Bytes())
				return
			}
			if partial{
				if _,err:=a.finishRestoredReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("partial receipt")}
				if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("partial terminal witness")}
				if err:=a.applyRestoredNaming(ctx,grant,g,naming,extras);err==nil{t.Fatal("partial naming replay")}
				checkBytes(diag,buf.Bytes())
				if mode=="revoked after link"||mode=="DB move failure"||mode=="revoked after DB move"{checkBytes(final,buf.Bytes())}
				var actual string
				if err:=g.DB.Read.QueryRow(`SELECT canonical_path FROM images WHERE id=?`,id).Scan(&actual);err!=nil{t.Fatal(err)}
				want:=diag;if mode=="revoked after DB move"{want=final}
				if actual!=want{t.Fatal("wrong DB boundary",actual,want)}
				if mode!="DB move failure"&&!injected{t.Fatal("injection boundary not reached")}
				return
			}
			checkBytes(final,buf.Bytes())
			if _,err:=os.Stat(diag);!os.IsNotExist(err){t.Fatal("diagnostic path remains",err)}
			if _,err:=os.Stat(old);!os.IsNotExist(err){t.Fatal("old missing path recreated",err)}
			if count(`SELECT COUNT(*) FROM images WHERE id=? AND canonical_path=? AND is_missing=0 AND note='operator note' AND original_source='https://example.invalid/operator-original' AND is_favorited=1 AND is_inbox=0 AND series='original-home' AND series_order=3 AND origin=?`,id,final,origin)!=1{t.Fatal("restored identity or state changed")}
			wantOld:=0;if marked{wantOld=1}
			if count(`SELECT COUNT(*) FROM image_paths WHERE path=? AND image_id=? AND is_canonical=0 AND id=?`,old,id,oldPathID)!=wantOld{t.Fatal("old missing row disposition")}
			if mode=="manual annotation"&&count(`SELECT COUNT(*) FROM image_annotations WHERE image_id=? AND manual=1 AND body='operator annotation'`,id)!=1{t.Fatal("manual annotation lost")}
			if count(`SELECT COUNT(*) FROM image_tags it JOIN tags t ON t.id=it.tag_id WHERE it.image_id=? AND t.name='prior'`,id)!=1{t.Fatal("prior tag lost")}
			rating,err:=readImageRating(ctx,g,id);if err!=nil||rating!=wantRating{t.Fatal("rating",rating,err)}
			stored,err:=a.finishRestoredReceipt(ctx,grant,g,naming,extras)
			if mode=="receipt revoked"{
				if err==nil{t.Fatal("acknowledged revoked receipt")}
				if _,err:=os.Stat(receipt);err!=nil{t.Fatal("receipt boundary",err)}
				stored,err=a.observeRestoredReceipt(ctx,grant,g,naming,extras)
			}
			rel,relErr:=filepath.Rel(s.GalleryRoot,final)
			if err!=nil||relErr!=nil||stored.Kind!=restoredReceiptKind||stored.Row.ImageID!=id||stored.Row.Name!=rel||stored.PriorRating!=priorRating||stored.OldMarkedMissing!=marked||stored.OldPathID!=oldPathID||!validDigest(stored.PreservationSHA256){t.Fatal("receipt",stored,err)}
			if _,err:=a.finishRestoredReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("receipt overwritten")}
			if _,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("wrong new-image receipt")}
			if _,err:=a.finishHeldReceipt(ctx,grant,g,extras);err==nil{t.Fatal("wrong held receipt")}
			if err:=a.applyClientExtras(ctx,grant,g,naming,extras);err==nil{t.Fatal("new-only extras accepted restoration")}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
			if mode=="DB reopen"{g.Close();g,err=library.Open(cfg);if err!=nil{t.Fatal(err)}}
			switch mode{
			case "lost naming result":if err:=os.Remove(result);err!=nil{t.Fatal(err)}
			case "damaged naming result":if err:=os.WriteFile(result,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "changed canonical":if err:=os.WriteFile(final,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "same bytes replacement":
				if err:=os.Rename(final,final+".retained");err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(final,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			case "old path returns after receipt":if err:=os.WriteFile(old,[]byte("operator"),0600);err!=nil{t.Fatal(err)}
			case "changed old row":if _,err:=g.DB.Write.Exec(`UPDATE image_paths SET id=id+10000 WHERE path=?`,old);err!=nil{t.Fatal(err)}
			case "changed final row":if _,err:=g.DB.Write.Exec(`UPDATE image_paths SET id=id+10000 WHERE path=?`,final);err!=nil{t.Fatal(err)}
			case "changed source":if _,err:=g.DB.Write.Exec(`UPDATE image_sources SET commentary='changed' WHERE image_id=? AND site='other'`,id);err!=nil{t.Fatal(err)}
			case "changed note":if _,err:=g.DB.Write.Exec(`UPDATE images SET note='changed' WHERE id=?`,id);err!=nil{t.Fatal(err)}
			case "changed rating":if _,err:=g.DB.Write.Exec(`DELETE FROM image_tags WHERE image_id=? AND tag_id IN(SELECT t.id FROM tags t JOIN tag_categories c ON c.id=t.category_id WHERE c.name='rating')`,id);err!=nil{t.Fatal(err)}
			case "changed membership":if _,err:=g.DB.Write.Exec(`UPDATE image_collections SET position=9 WHERE image_id=?`,id);err!=nil{t.Fatal(err)}
			case "changed job":job.Commentary="unapplied"
			case "changed naming":req.DefaultName="changed"
			case "damaged receipt":if err:=os.WriteFile(receipt,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			}
			recovered,err:=a.observeRestoredReceipt(ctx,grant,g,naming,extras)
			bad:=strings.HasPrefix(mode,"changed")||mode=="same bytes replacement"||mode=="old path returns after receipt"||mode=="lost naming result"||mode=="damaged naming result"||mode=="damaged receipt"
			if (err!=nil)!=bad||(!bad&&recovered!=stored){t.Fatal("unexpected recovery",err)}
			witness,werr:=a.observeTerminalReceipt(ctx,grant,g,naming,extras)
			if (werr!=nil)!=bad{t.Fatal("common terminal witness recovery",werr)}
			if !bad{
				body,err:=os.ReadFile(receipt)
				wantRow:=stored.Row
				if err!=nil||witness.Kind!=stored.Kind||witness.Allocation!=grant||witness.GalleryIdentity!=stored.GalleryIdentity||witness.PolicySHA256!=stored.PolicySHA256||witness.ReceiptSHA256!=digestBytes(body)||witness.Row!=wantRow||witness.PathID<=0||witness.FileIdentity!=stored.FileIdentity{t.Fatal("common witness differs",witness,err)}
			}
			if err:=a.applyRestoredNaming(ctx,grant,g,naming,extras);err==nil{t.Fatal("naming replay")}
			if mode=="old path returns after receipt"{checkBytes(old,[]byte("operator"))}
			if mode=="collision"{checkBytes(filepath.Join(s.GalleryRoot,"derpibooru-42.png"),[]byte("operator"))}
			checkBytes(filepath.Join(dir,"media",f.RelativeName),buf.Bytes())
			checkBytes(filepath.Join(stage,grant.Request.OperationID,"sealed","media",f.RelativeName),buf.Bytes())
		})
	}
}
