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

func TestWorkspaceReclamationThroughActualReceipts(t *testing.T){
	for _,mode:=range []string{"new","collection","held","restored","DB reopen","missing receipt","no authority","denied","changed original","foreign link","extra file","changed manifest",
		"revoked after intent","revoked after first","revoked after both","revoked after result","changed survivor","replaced survivor","wrong prefix","replaced directory","changed canonical","changed job","damaged attempt","damaged result","reappeared file","lost result"}{
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
			attempt:=filepath.Join(op,"publication","workspace-reclaim-attempt.json")
			result:=filepath.Join(op,"publication","workspace-reclaim-result.json")
			sealed:=filepath.Join(op,"sealed","media",f.RelativeName)
			prepared:=filepath.Join(op,"publication","media",f.RelativeName)
			manifest:=filepath.Join(workspace,"strict-blacklist.json");probe:=filepath.Join(workspace,"strict-blacklist-probe.json")
			manifestBytes,err:=os.ReadFile(manifest);if err!=nil{t.Fatal(err)}
			probeBytes,err:=os.ReadFile(probe);if err!=nil{t.Fatal(err)}
			sidecarBytes,err:=os.ReadFile(sidecar);if err!=nil{t.Fatal(err)}
			checkBytes:=func(p string,want []byte){t.Helper();got,err:=os.ReadFile(p);if err!=nil||!bytes.Equal(got,want){t.Fatal("bytes",p,err)}}
			absent:=func(p string)bool{_,err:=os.Lstat(p);return os.IsNotExist(err)}
			authorize:=reclaimAuthority(func(_ context.Context,q workerproto.StageAllocation,w terminalReceiptWitness)error{
				if q!=grant||w.Allocation!=grant||w.Row.ImageID!=mapped[0].ImageID||w.Row.SHA256!=f.SHA256{t.Fatal("wrong authorization witness")}
				return nil
			})
			switch mode{
			case "no authority":authorize=nil
			case "denied":authorize=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return ErrStageRefused}
			case "changed original":if err:=os.WriteFile(original,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "foreign link":if err:=os.Link(original,filepath.Join(workspace,"foreign"));err!=nil{t.Fatal(err)}
			case "extra file":if err:=os.WriteFile(filepath.Join(media,"foreign.txt"),[]byte("operator"),0600);err!=nil{t.Fatal(err)}
			case "changed manifest":if err:=os.WriteFile(manifest,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			}
			pre:=mode=="missing receipt"||mode=="no authority"||mode=="denied"||mode=="changed original"||mode=="foreign link"||mode=="extra file"||mode=="changed manifest"
			partial:=strings.HasPrefix(mode,"revoked after")||mode=="changed survivor"||mode=="replaced survivor"||mode=="wrong prefix"||mode=="replaced directory"||mode=="changed canonical"||mode=="changed job"||mode=="damaged attempt"
			injected:=false
			if partial{
				ok:=authorize
				authorize=func(ctx context.Context,q workerproto.StageAllocation,w terminalReceiptWitness)error{
					if err:=ok(ctx,q,w);err!=nil{return err}
					if injected{return ErrStageRefused}
					switch mode{
					case "revoked after first","changed survivor","replaced survivor":
						if absent(original)&&!absent(sidecar){injected=true;return ErrStageRefused}
					case "revoked after both":
						if absent(original)&&absent(sidecar){injected=true;return ErrStageRefused}
					case "revoked after result":
						if !absent(result){injected=true;return ErrStageRefused}
					default:
						if !absent(attempt){injected=true;return ErrStageRefused}
					}
					return nil
				}
			}
			if _,err:=a.observeWorkspaceReclaim(ctx,grant,g,naming,extras,authorize);err==nil{t.Fatal("reclaim observed without intent")}
			got,err:=a.reclaimWorkspace(ctx,grant,g,naming,extras,authorize)
			if (err!=nil)!=(pre||partial){t.Fatal("reclaim",got,err)}
			checkBytes(final,buf.Bytes());checkBytes(prepared,buf.Bytes());checkBytes(sealed,buf.Bytes())
			if pre{
				if !absent(attempt)||absent(original)||absent(sidecar){t.Fatal("precondition refusal mutated workspace")}
				return
			}
			if partial{
				if !injected{t.Fatal("injection not reached")}
				if mode!="revoked after result"&&!absent(result){t.Fatal("partial acknowledged")}
				if _,err:=a.reclaimWorkspace(ctx,grant,g,naming,extras,authorize);err==nil{t.Fatal("fresh begin replayed")}
				// Reopen with a fresh, independently authorizing custody seam.
				if err:=a.Close();err!=nil{t.Fatal(err)}
				a,err=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
				authorize=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return nil}
				switch mode{
				case "changed survivor":if err:=os.WriteFile(sidecar,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
				case "replaced survivor":
					if err:=os.Rename(sidecar,sidecar+".retained");err!=nil{t.Fatal(err)}
					if err:=os.WriteFile(sidecar,sidecarBytes,0600);err!=nil{t.Fatal(err)}
				case "wrong prefix":if err:=os.Remove(sidecar);err!=nil{t.Fatal(err)}
				case "replaced directory":
					if err:=os.Rename(media,filepath.Join(workspace,"old-media"));err!=nil{t.Fatal(err)}
					if err:=os.Mkdir(media,0700);err!=nil{t.Fatal(err)}
				case "changed canonical":if err:=os.WriteFile(final,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
				case "changed job":job.Commentary="changed"
				case "damaged attempt":if err:=os.WriteFile(attempt,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
				}
				got,err=a.resumeWorkspaceReclaim(ctx,grant,g,naming,extras,authorize)
				bad:=!strings.HasPrefix(mode,"revoked after")
				if (err!=nil)!=bad{t.Fatal("resume",got,err)}
				if bad{
					if !absent(result){t.Fatal("unsafe result")}
					if mode=="changed survivor"{checkBytes(sidecar,[]byte("changed"))}
					if mode=="replaced survivor"{checkBytes(sidecar,sidecarBytes)}
					checkBytes(sealed,buf.Bytes())
					return
				}
			}
			if got.RemovedBytes!=f.Size+f.SidecarSize||!absent(original)||!absent(sidecar){t.Fatal("workspace result",got)}
			checkBytes(manifest,manifestBytes);checkBytes(probe,probeBytes)
			checkBytes(final,buf.Bytes());checkBytes(prepared,buf.Bytes());checkBytes(sealed,buf.Bytes())
			if _,err:=a.Workspace(ctx,grant);err==nil{t.Fatal("workspace reopened for writer")}
			if again,err:=a.Allocate(ctx,grant.Request);err!=nil||again!=grant{t.Fatal("reservation lost",err)}
			// Reclamation does not release the operation slot or payload reservation.
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,authority);if err!=nil{t.Fatal(err)}
			if mode=="DB reopen"{g.Close();g,err=library.Open(cfg);if err!=nil{t.Fatal(err)}}
			switch mode{
			case "damaged result":if err:=os.WriteFile(result,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "reappeared file":if err:=os.WriteFile(original,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			case "lost result":if err:=os.Remove(result);err!=nil{t.Fatal(err)}
			}
			observed,err:=a.observeWorkspaceReclaim(ctx,grant,g,naming,extras,authorize)
			bad:=mode=="damaged result"||mode=="reappeared file"||mode=="lost result"
			if (err!=nil)!=bad||(!bad&&observed!=got){t.Fatal("reclaim observation",observed,err)}
			if mode=="lost result"{
				recovered,err:=a.resumeWorkspaceReclaim(ctx,grant,g,naming,extras,authorize)
				if err!=nil||recovered!=got{t.Fatal("lost result recovery",err)}
			}
			if mode=="reappeared file"{checkBytes(original,buf.Bytes())}
			checkBytes(sealed,buf.Bytes())
		})
	}
}
