package acquisitionstage

import (
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
)

func TestComposedNewReceiptWithClientExtras(t *testing.T){
	for _,mode:=range []string{"new tags","empty job","shared ownership","rejected tag","implication","rated filter","DB reopen","alias","rating alias","source label collision",
		"unrated rating","rating implication","custom via","commentary","source override","no authority",
		"revoked after attempt","DB failure","revoked after write","changed job","changed naming",
		"lost extras result","damaged extras result","missing client owner","unexpected tag","changed implication","damaged receipt","receipt revocation",
		"collection","collection existing label","collection missing witness","collection changed before move","collection changed after move",
		"collection changed after receipt","collection extra member","collection lost result","collection damaged result"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,authority,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",
				Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}}
			if mode=="unrated rating"{row.Rating="";row.Tags=[]string{"fixture"}}
			if mode=="source label collision"{row.SourceLabel=contract.Via}
			withCollection:=strings.HasPrefix(mode,"collection")
			if withCollection{row.Provenance.Collection="pool";row.Provenance.CollectionOrder=7}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			g,err:=library.Open(cfg);if err!=nil{t.Fatal(err)}
			defer func(){g.Close()}()
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			if err:=a.landNewPrepared(ctx,grant,g);err!=nil{t.Fatal(err)}
			mapped,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(mapped)!=1{t.Fatal("metadata",err)}
			id:=mapped[0].ImageID
			if err:=a.applyRelationships(ctx,grant,g);err!=nil{t.Fatal(err)}
			wantCollection:="pool"
			if mode=="collection existing label"{
				other:=image.NewRGBA(image.Rect(0,0,17,17))
				var b bytes.Buffer
				if err:=png.Encode(&b,other);err!=nil{t.Fatal(err)}
				res,err:=g.Land(ctx,s.GalleryRoot,"existing.png",library.FromReader(bytes.NewReader(b.Bytes())),library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("collection label fixture",err)}
				order:=2
				if err:=gallery.AddCollectionMembership(g.DB,res.Image.ID,"POOL",&order);err!=nil{t.Fatal(err)}
				wantCollection="POOL"
			}
			if withCollection&&mode!="collection missing witness"{if err:=a.applyCollections(ctx,grant,g);err!=nil{t.Fatal(err)}}
			if mode=="collection changed before move"{if _,err:=g.DB.Write.Exec(`UPDATE image_collections SET position=8 WHERE image_id=?`,id);err!=nil{t.Fatal(err)}}
			req:=gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:f.RelativeName,Source:row.SourceLabel,PostID:row.PostID,DefaultName:"{source}-{post_id}"}
			naming:=func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return req,nil}
			err=a.applyNaming(ctx,grant,g,naming)
			if mode=="collection missing witness"||mode=="collection changed before move"{
				if err==nil{t.Fatal("naming accepted incomplete collection")}
				if _,err:=os.Stat(filepath.Join(stage,grant.Request.OperationID,"publication","naming-attempt.json"));!os.IsNotExist(err){t.Fatal("unexpected naming attempt",err)}
				return
			}
			if err!=nil{t.Fatal(err)}
			if mode=="collection changed after move"{if _,err:=g.DB.Write.Exec(`UPDATE images SET series_order=8 WHERE id=?`,id);err!=nil{t.Fatal(err)}}
			dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
			attempt:=filepath.Join(dir,"extras-attempt.json")
			result:=filepath.Join(dir,"extras-result.json")
			receipt:=filepath.Join(dir,"composed-receipt.json")
			final:=filepath.Join(s.GalleryRoot,"derpibooru-42.png")
			job:=contract.JobExtras{Tags:[]string{"Client Tag"}}
			var parent,child int64
			tag:=func(form string)int64{
				t.Helper()
				cat,bare,err:=gallery.ResolveCategoryTag(g.DB,form)
				if err!=nil{t.Fatal(err)}
				x,err:=g.TagSvc.GetOrCreateTag(bare,cat)
				if err!=nil{t.Fatal(err)}
				return x.ID
			}
			switch mode{
			case "empty job":job=contract.JobExtras{}
			case "shared ownership":job.Tags=append(job.Tags,"fixture","client_tag")
			case "rejected tag":
				cat,bare,err:=gallery.ResolveCategoryTag(g.DB,"refused")
				if err!=nil{t.Fatal(err)}
				if _,err:=g.TagSvc.RejectTagName(bare,cat);err!=nil{t.Fatal(err)}
				job.Tags=append(job.Tags,"refused")
			case "implication","changed implication","rating implication":
				parent=tag("client_tag")
				child=tag("implied_child")
				if mode=="rating implication"{child=tag("rating:explicit")}
				if _,err:=g.TagSvc.AddImplication(parent,child);err!=nil{t.Fatal(err)}
			case "alias","rating alias":
				canon:=tag("canonical_client")
				if mode=="rating alias"{canon=tag("rating:explicit")}
				cat,bare,err:=gallery.ResolveCategoryTag(g.DB,"client_tag")
				if err!=nil{t.Fatal(err)}
				if _,err:=g.TagSvc.CreateAlias(bare,cat,canon);err!=nil{t.Fatal(err)}
			case "rated filter","unrated rating":job.Tags=append(job.Tags,"rating:explicit")
			case "custom via":job.Via="derpibooru"
			case "commentary":job.Commentary="client fallback"
			case "source override":job.Source="other"
			case "DB failure":
				if _,err:=g.DB.Write.Exec(`CREATE TRIGGER refuse_client BEFORE INSERT ON image_tag_sources WHEN NEW.source='monloader' BEGIN SELECT RAISE(ABORT,'fixture'); END`);err!=nil{t.Fatal(err)}
			}
			revoked:=false
			extras:=extrasAuthority(func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){
				if mode=="revoked after attempt"{
					if _,err:=os.Stat(attempt);err==nil&&!revoked{revoked=true;return job,ErrStageRefused}
				}
				if mode=="revoked after write"{
					var n int
					if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_tag_sources WHERE image_id=? AND source=?`,id,contract.Via).Scan(&n);err!=nil{t.Fatal(err)}
					if n>0&&!revoked{revoked=true;return job,ErrStageRefused}
				}
				if mode=="receipt revocation"{
					if _,err:=os.Stat(receipt);err==nil&&!revoked{revoked=true;return job,ErrStageRefused}
				}
				return job,nil
			})
			if mode=="no authority"{extras=nil}
			if _,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("receipt without explicit extras witness")}
			if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("terminal witness before receipt")}
			err=a.applyClientExtras(ctx,grant,g,naming,extras)
			preRefusal:=mode=="collection changed after move"||mode=="rating alias"||mode=="source label collision"||mode=="unrated rating"||mode=="rating implication"||mode=="custom via"||mode=="commentary"||mode=="source override"||mode=="no authority"
			partial:=mode=="revoked after attempt"||mode=="DB failure"||mode=="revoked after write"
			if (err!=nil)!=(preRefusal||partial){t.Fatalf("unexpected extras result %v",err)}
			count:=func(query string,args ...any)int{
				t.Helper();var n int
				if err:=g.DB.Read.QueryRow(query,args...).Scan(&n);err!=nil{t.Fatal(err)}
				return n
			}
			clientCount:=count(`SELECT COUNT(*) FROM image_tag_sources WHERE image_id=? AND source=?`,id,contract.Via)
			rating,err:=readImageRating(ctx,g,id)
			if err!=nil||rating!=row.Rating{t.Fatal("source rating changed",rating,err)}
			if preRefusal{
				if _,err:=os.Stat(attempt);!os.IsNotExist(err){t.Fatal("unexpected extras attempt",err)}
				if mode!="source label collision"&&clientCount!=0{t.Fatal("unauthorized tags")}
				if mode=="source label collision"&&clientCount!=len(mapped[0].TagIDs){t.Fatal("source ledger changed")}
				return
			}
			if partial{
				if _,err:=os.Stat(result);!os.IsNotExist(err){t.Fatal("unexpected extras result",err)}
				if mode=="revoked after write"{if clientCount!=1{t.Fatal("write boundary not reached")}}else if clientCount!=0{t.Fatal("unauthorized client tag")}
				if _,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("partial receipt")}
				if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("partial terminal witness")}
				if err:=a.applyClientExtras(ctx,grant,g,naming,extras);err==nil{t.Fatal("partial replay")}
				return
			}
			wantCount:=1
			if mode=="empty job"{wantCount=0}
			if mode=="shared ownership"{wantCount=2}
			if clientCount!=wantCount{t.Fatal("unexpected client ownership",clientCount)}
			got,err:=a.observeClientExtras(ctx,grant,g,naming,extras)
			if err!=nil||len(got.Client.TagIDs)!=wantCount{t.Fatal("extras observation",err)}
			if mode=="rejected tag"&&!slices.Equal(got.Rejected,[]string{"refused"}){t.Fatal("rejection not recorded",got.Rejected)}
			if count(`SELECT COUNT(*) FROM image_tag_sources ts JOIN tags t ON t.id=ts.tag_id WHERE ts.image_id=? AND ts.source=? AND t.name='fixture'`,id,row.SourceLabel)!=1{t.Fatal("source ownership lost")}
			if mode=="implication"||mode=="changed implication"{
				if count(`SELECT COUNT(*) FROM image_tags WHERE image_id=? AND tag_id=? AND is_implied=1`,id,child)!=1{t.Fatal("implication missing")}
			}
			stored,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras)
			if mode=="receipt revocation"{
				if err==nil{t.Fatal("acknowledged revoked receipt")}
				if _,err:=os.Stat(receipt);err!=nil{t.Fatal("receipt boundary not reached",err)}
				stored,err=a.observeComposedReceipt(ctx,grant,g,naming,extras)
			}
			if err!=nil||stored.Kind!=composedReceiptKind||stored.Row.ImageID!=id||stored.Row.Name!="derpibooru-42.png"||stored.Row.SHA256!=f.SHA256{t.Fatal("composed receipt",stored,err)}
			if withCollection{
				if !validDigest(stored.CollectionSHA256)||count(`SELECT COUNT(*) FROM images WHERE id=? AND series=? COLLATE BINARY AND series_order=7`,id,wantCollection)!=1||count(`SELECT COUNT(*) FROM image_collections WHERE image_id=? AND name=? COLLATE BINARY AND position=7`,id,wantCollection)!=1{t.Fatal("collection not composed")}
			}else if stored.CollectionSHA256!=""{t.Fatal("unexpected collection digest")}
			if _,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("overwrote receipt")}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
			if mode=="DB reopen"{g.Close();g,err=library.Open(cfg);if err!=nil{t.Fatal(err)}}
			switch mode{
			case "changed job":job.Tags=[]string{"other"}
			case "changed naming":req.DefaultName="changed"
			case "lost extras result":if err:=os.Remove(result);err!=nil{t.Fatal(err)}
			case "damaged extras result":if err:=os.WriteFile(result,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "missing client owner":
				if _,err:=g.DB.Write.Exec(`DELETE FROM image_tag_sources WHERE image_id=? AND source=?`,id,contract.Via);err!=nil{t.Fatal(err)}
			case "unexpected tag":
				if _,_,_,err:=g.ApplyInitialTags(id,[]string{"foreign"},"other");err!=nil{t.Fatal(err)}
			case "changed implication":if err:=g.TagSvc.RemoveImplication(parent,child);err!=nil{t.Fatal(err)}
			case "damaged receipt":if err:=os.WriteFile(receipt,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "collection changed after receipt":if _,err:=g.DB.Write.Exec(`UPDATE image_collections SET position=8 WHERE image_id=?`,id);err!=nil{t.Fatal(err)}
			case "collection extra member":if err:=gallery.AddCollectionMembership(g.DB,id,"other",nil);err!=nil{t.Fatal(err)}
			case "collection lost result":if err:=os.Remove(filepath.Join(dir,"collection-result.json"));err!=nil{t.Fatal(err)}
			case "collection damaged result":if err:=os.WriteFile(filepath.Join(dir,"collection-result.json"),[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			}
			recovered,err:=a.observeComposedReceipt(ctx,grant,g,naming,extras)
			bad:=mode=="changed job"||mode=="changed naming"||mode=="lost extras result"||mode=="damaged extras result"||
				mode=="missing client owner"||mode=="unexpected tag"||mode=="changed implication"||mode=="damaged receipt"||
				mode=="collection changed after receipt"||mode=="collection extra member"||mode=="collection lost result"||mode=="collection damaged result"
			if (err!=nil)!=bad||(!bad&&recovered!=stored){t.Fatal("unexpected receipt recovery",err)}
			witness,werr:=a.observeTerminalReceipt(ctx,grant,g,naming,extras)
			if (werr!=nil)!=bad{t.Fatal("common terminal witness recovery",werr)}
			if !bad{
				body,err:=os.ReadFile(receipt)
				wantRow:=stored.Row
				if err!=nil||witness.Kind!=stored.Kind||witness.Allocation!=grant||witness.GalleryIdentity!=stored.GalleryIdentity||witness.PolicySHA256!=stored.PolicySHA256||witness.ReceiptSHA256!=digestBytes(body)||witness.Row!=wantRow||witness.PathID<=0||witness.FileIdentity!=stored.FileIdentity{t.Fatal("common witness differs",witness,err)}
			}
			if err:=a.applyClientExtras(ctx,grant,g,naming,extras);err==nil{t.Fatal("replayed extras")}
			for _,path:=range []string{final,filepath.Join(dir,"media",f.RelativeName),filepath.Join(stage,grant.Request.OperationID,"sealed","media",f.RelativeName)}{
				b,err:=os.ReadFile(path);if err!=nil||!bytes.Equal(b,buf.Bytes()){t.Fatal("media evidence lost",path,err)}
			}
		})
	}
}
