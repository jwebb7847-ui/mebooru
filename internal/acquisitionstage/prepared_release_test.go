package acquisitionstage

import (
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

func TestPreparedReleaseThroughActualReceipts(t *testing.T){
	for _,mode:=range []string{"new","held","restored","collection","DB reopen","before workspace","no authority","denied","foreign link",
		"revoked after intent","revoked after unlink","revoked after result","changed before unlink","changed canonical","same bytes replacement","changed job","changed row","changed sealed","changed workspace result","damaged attempt","lost attempt","damaged result","lost result","reappeared prepared","replaced media root"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,authority,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",SourceLabel:"derpibooru",
				PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}}
			if mode=="collection"{row.Provenance.Collection="pool";row.Provenance.CollectionOrder=7}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			g,err:=library.Open(cfg);if err!=nil{t.Fatal(err)}
			defer func(){g.Close()}()
			old:=filepath.Join(s.GalleryRoot,"existing.png")
			if mode=="held"||mode=="restored"{
				if err:=os.WriteFile(old,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,old,library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("original",err)}
				if mode=="restored"{
					if err:=os.Remove(old);err!=nil{t.Fatal(err)}
					if _,err:=gallery.SetMissing(g.DB,res.Image.ID,true);err!=nil{t.Fatal(err)}
				}
			}
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			switch mode{
			case "held":
				if err:=a.beginLanding(ctx,grant,g);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,filepath.Join(s.GalleryRoot,finalName(grant,f)),library.LandSpec{Origin:"fixture"})
				if err!=nil||!res.Duplicate{t.Fatal("held",err)}
			case "restored":if err:=a.landRestoredPrepared(ctx,grant,g);err!=nil{t.Fatal(err)}
			default:if err:=a.landNewPrepared(ctx,grant,g);err!=nil{t.Fatal(err)}
			}
			mapped,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(mapped)!=1{t.Fatal("metadata",err)}
			if err:=a.applyRelationships(ctx,grant,g);err!=nil{t.Fatal(err)}
			if mode=="collection"{if err:=a.applyCollections(ctx,grant,g);err!=nil{t.Fatal(err)}}
			req:=gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:f.RelativeName,Source:row.SourceLabel,PostID:row.PostID,DefaultName:"{source}-{post_id}"}
			naming:=namingAuthority(func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return req,nil})
			job:=contract.JobExtras{}
			extras:=extrasAuthority(func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){return job,nil})
			final:=filepath.Join(s.GalleryRoot,"derpibooru-42.png")
			switch mode{
			case "held":
				final=old
				if _,err:=a.disposeComposedHeld(ctx,grant,g,extras);err!=nil{t.Fatal(err)}
				if _,err:=a.finishHeldReceipt(ctx,grant,g,extras);err!=nil{t.Fatal(err)}
			case "restored":
				if err:=a.applyRestoredNaming(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}
				if _,err:=a.finishRestoredReceipt(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}
			default:
				if err:=a.applyNaming(ctx,grant,g,naming);err!=nil{t.Fatal(err)}
				if err:=a.applyClientExtras(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}
				if mode!="missing receipt"{if _,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}}
			}
			op:=filepath.Join(stage,grant.Request.OperationID)
			workspace:=filepath.Join(op,grant.TransferID);media:=filepath.Join(workspace,"media")
			original:=filepath.Join(media,f.RelativeName);sidecar:=original+".json"
			attempt:=filepath.Join(op,"publication","prepared-release-attempt.json")
			result:=filepath.Join(op,"publication","prepared-release-result.json")
			sealed:=filepath.Join(op,"sealed","media",f.RelativeName)
			prepared:=filepath.Join(op,"publication","media",f.RelativeName)
			reclaim:=reclaimAuthority(func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return nil})
			if mode!="before workspace"{
				if _,err:=a.reclaimWorkspace(ctx,grant,g,naming,extras,reclaim);err!=nil{t.Fatal("workspace reclaim",err)}
			}
			absent:=func(path string)bool{_,err:=os.Lstat(path);return os.IsNotExist(err)}
			checkBytes:=func(path string,want []byte){t.Helper();got,err:=os.ReadFile(path);if err!=nil||!bytes.Equal(got,want){t.Fatal("bytes",path,err)}}
			auth:=preparedReleaseAuthority(func(_ context.Context,q workerproto.StageAllocation,w terminalReceiptWitness)error{
				if q!=grant||w.Allocation!=grant||w.Row.ImageID!=mapped[0].ImageID||w.Row.SHA256!=f.SHA256{t.Fatal("wrong release witness")}
				return nil
			})
			if mode=="no authority"{auth=nil}
			if mode=="denied"{auth=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return ErrStageRefused}}
			if mode=="foreign link"{if err:=os.Link(prepared,filepath.Join(filepath.Dir(stage),"foreign"));err!=nil{t.Fatal(err)}}
			pre:=mode=="before workspace"||mode=="no authority"||mode=="denied"||mode=="foreign link"
			partial:=strings.HasPrefix(mode,"revoked after")||mode=="changed before unlink"
			injected:=false
			if partial{
				auth=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{
					if injected{return ErrStageRefused}
					switch mode{
					case "revoked after intent","changed before unlink":
						if !absent(attempt){injected=true;return ErrStageRefused}
					case "revoked after unlink":
						if absent(prepared){injected=true;return ErrStageRefused}
					case "revoked after result":
						if !absent(result){injected=true;return ErrStageRefused}
					}
					return nil
				}
			}
			if _,err:=a.observePreparedRelease(ctx,grant,g,naming,extras,auth);err==nil{t.Fatal("release observed before intent")}
			got,err:=a.releasePrepared(ctx,grant,g,naming,extras,auth)
			if (err!=nil)!=(pre||partial){t.Fatal("release",got,err)}
			checkBytes(final,buf.Bytes());checkBytes(sealed,buf.Bytes())
			if pre{
				if !absent(attempt)||absent(prepared){t.Fatal("precondition refusal mutated prepared file")}
				checkBytes(prepared,buf.Bytes())
				return
			}
			if !absent(original)||!absent(sidecar){t.Fatal("original workspace payload restored")}
			if partial{
				if !injected{t.Fatal("injection not reached")}
				if mode!="revoked after result"&&!absent(result){t.Fatal("partial acknowledged")}
				before:=mode=="revoked after intent"||mode=="changed before unlink"
				if absent(prepared)==before{t.Fatal("wrong unlink boundary")}
				if _,err:=a.releasePrepared(ctx,grant,g,naming,extras,auth);err==nil{t.Fatal("fresh release replayed")}
				if err:=a.Close();err!=nil{t.Fatal(err)}
				a,err=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
				auth=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return nil}
				if mode=="changed before unlink"{
					if err:=os.WriteFile(final,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
					if _,err:=a.resumePreparedRelease(ctx,grant,g,naming,extras,auth);err==nil{t.Fatal("changed before unlink accepted")}
					checkBytes(prepared,[]byte("changed"));checkBytes(sealed,buf.Bytes())
					return
				}
				got,err=a.resumePreparedRelease(ctx,grant,g,naming,extras,auth)
				if err!=nil{t.Fatal("resume",err)}
			}
			if !absent(prepared)||got.ReleasedEntryBytes!=f.Size||got.SharedCanonical!=(mode!="held"){t.Fatal("release result",got)}
			checkBytes(final,buf.Bytes());checkBytes(sealed,buf.Bytes())
			if _,err:=a.observeTerminalReceipt(ctx,grant,g,naming,extras);err==nil{t.Fatal("old full-byte observer accepted missing prepared file")}
			// Allocation stays reserved; no reacquisition or writer restart.
			if _,err:=a.Workspace(ctx,grant);err==nil{t.Fatal("workspace reopened")}
			if _,reserved,err:=a.inventory(ctx);err!=nil||reserved!=grant.MaxBytes{t.Fatal("reservation changed",reserved,err)}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
			if mode=="DB reopen"{g.Close();g,err=library.Open(cfg);if err!=nil{t.Fatal(err)}}
			switch mode{
			case "changed canonical":if err:=os.WriteFile(final,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "same bytes replacement":
				if err:=os.Rename(final,filepath.Join(filepath.Dir(stage),"canonical.retained"));err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(final,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			case "changed job":job.Commentary="changed"
			case "changed row":if _,err:=g.DB.Write.Exec(`UPDATE image_paths SET id=id+10000 WHERE path=?`,final);err!=nil{t.Fatal(err)}
			case "changed sealed":if err:=os.WriteFile(sealed,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "changed workspace result":if err:=os.WriteFile(filepath.Join(op,"publication","workspace-reclaim-result.json"),[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "damaged attempt":if err:=os.WriteFile(attempt,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "lost attempt":if err:=os.Remove(attempt);err!=nil{t.Fatal(err)}
			case "damaged result":if err:=os.WriteFile(result,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "lost result":if err:=os.Remove(result);err!=nil{t.Fatal(err)}
			case "reappeared prepared":if err:=os.Link(final,prepared);err!=nil{t.Fatal(err)}
			case "replaced media root":
				if err:=os.Rename(filepath.Dir(prepared),filepath.Join(filepath.Dir(stage),"prepared-media.retained"));err!=nil{t.Fatal(err)}
				if err:=os.Mkdir(filepath.Dir(prepared),0700);err!=nil{t.Fatal(err)}
			}
			observed,err:=a.observePreparedRelease(ctx,grant,g,naming,extras,auth)
			bad:=strings.HasPrefix(mode,"changed")||mode=="same bytes replacement"||mode=="damaged attempt"||mode=="lost attempt"||mode=="damaged result"||mode=="lost result"||mode=="reappeared prepared"||mode=="replaced media root"
			if (err!=nil)!=bad||(!bad&&observed!=got){t.Fatal("release observation",observed,err)}
			if mode=="lost result"{
				recovered,err:=a.resumePreparedRelease(ctx,grant,g,naming,extras,auth)
				if err!=nil||recovered!=got{t.Fatal("lost result recovery",err)}
			}
			if mode=="reappeared prepared"{checkBytes(prepared,buf.Bytes())}
			if mode!="changed sealed"{checkBytes(sealed,buf.Bytes())}
		})
	}
}
