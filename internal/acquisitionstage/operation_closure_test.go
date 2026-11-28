package acquisitionstage

import (
	"bytes"
	"context"
	"errors"
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

func TestOperationClosureReleasesOnlyActiveCapacity(t *testing.T){
	for _,mode:=range []string{"new","held","restored","collection","DB reopen","before retirement","no authority","denied","revoked after closure","damaged closure","lost closure","reappeared sealed","changed retained sidecar","changed policy","operator edit","operator deletion","retained capacity","exact replay refused"}{
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
				if mode=="held aliases"{
					alias:=filepath.Join(s.GalleryRoot,"operator-alias.png")
					if err:=os.WriteFile(alias,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
					v,err:=g.LandInPlace(ctx,alias,library.LandSpec{Origin:"fixture"})
					if err!=nil||!v.Duplicate||v.Image.ID!=res.Image.ID{t.Fatal("operator alias",err)}
				}
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
				if _,err:=a.finishComposedReceipt(ctx,grant,g,naming,extras);err!=nil{t.Fatal(err)}
			}
			op:=filepath.Join(stage,grant.Request.OperationID)
			workspace:=filepath.Join(op,grant.TransferID);media:=filepath.Join(workspace,"media")
			original:=filepath.Join(media,f.RelativeName);sidecar:=original+".json"
			result:=filepath.Join(op,"publication","operation-closed.json")
			sealed:=filepath.Join(op,"sealed","media",f.RelativeName)
			prepared:=filepath.Join(op,"publication","media",f.RelativeName)
			reclaim:=reclaimAuthority(func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return nil})
			release:=preparedReleaseAuthority(func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return nil})
			retire:=sealedRetirementAuthority(func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return nil})
			if _,err:=a.reclaimWorkspace(ctx,grant,g,naming,extras,reclaim);err!=nil{t.Fatal(err)}
			if _,err:=a.releasePrepared(ctx,grant,g,naming,extras,release);err!=nil{t.Fatal(err)}
			if mode!="before retirement"{if _,err:=a.retireSealedMedia(ctx,grant,g,naming,extras,retire);err!=nil{t.Fatal(err)}}
			native:=sealed+".json"
			nativeBytes,err:=os.ReadFile(native);if err!=nil{t.Fatal(err)}
			absent:=func(path string)bool{_,err:=os.Lstat(path);return os.IsNotExist(err)}
			checkBytes:=func(path string,want []byte){t.Helper();got,err:=os.ReadFile(path);if err!=nil||!bytes.Equal(got,want){t.Fatal("bytes",path,err)}}
			a.maxCount=1;a.maxBytes=grant.MaxBytes+uint64(f.SidecarSize)
			second:=grant.Request;second.OperationID=strings.Repeat("d",32);second.Transfer.Metadata.JobID++
			if _,err:=a.Allocate(ctx,second);!errors.Is(err,ErrStageLimit){t.Fatal("unclosed slot released",err)}
			auth:=closureAuthority(func(_ context.Context,q workerproto.StageAllocation,w terminalReceiptWitness)error{
				if q!=grant||w.Allocation!=grant||w.Row.ImageID!=mapped[0].ImageID||w.Row.SHA256!=f.SHA256{t.Fatal("wrong closure witness")}
				return nil
			})
			switch mode{
			case "no authority":auth=nil
			case "denied":auth=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return ErrStageRefused}
			case "revoked after closure":
				auth=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{if !absent(result){return ErrStageRefused};return nil}
			}
			if _,err:=a.observeClosedOperation(ctx,grant,g,naming,extras,auth);err==nil{t.Fatal("closure observed without record")}
			got,err:=a.closeRetiredOperation(ctx,grant,g,naming,extras,auth)
			pre:=mode=="before retirement"||mode=="no authority"||mode=="denied"
			if (err!=nil)!=(pre||mode=="revoked after closure"){t.Fatal("close",got,err)}
			checkBytes(final,buf.Bytes());checkBytes(native,nativeBytes)
			if !absent(original)||!absent(sidecar)||!absent(prepared){t.Fatal("prior payload reappeared")}
			if pre{
				if !absent(result){t.Fatal("precondition closure written")}
				if _,err:=a.Allocate(ctx,second);!errors.Is(err,ErrStageLimit){t.Fatal("unclosed capacity released",err)}
				return
			}
			if absent(result)||!absent(sealed){t.Fatal("missing closure or reappeared media")}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,1,grant.MaxBytes+uint64(f.SidecarSize),authority);if err!=nil{t.Fatal(err)}
			auth=func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error{return nil}
			if mode=="DB reopen"{g.Close();g,err=library.Open(cfg);if err!=nil{t.Fatal(err)}}
			observed,err:=a.observeClosedOperation(ctx,grant,g,naming,extras,auth)
			if err!=nil||observed.RetainedPayloadBytes!=uint64(f.SidecarSize){t.Fatal("closure observation",observed,err)}
			if mode!="revoked after closure"&&got!=observed{t.Fatal("closure mismatch")}
			if _,err:=a.closeRetiredOperation(ctx,grant,g,naming,extras,auth);err==nil{t.Fatal("closed operation replayed")}
			if _,err:=a.Workspace(ctx,grant);err==nil{t.Fatal("workspace reopened")}
			if _,err:=a.Allocate(ctx,grant.Request);!errors.Is(err,ErrStageRefused){t.Fatal("closed identity reused",err)}
			switch mode{
			case "damaged closure":if err:=os.WriteFile(result,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "lost closure":if err:=os.Remove(result);err!=nil{t.Fatal(err)}
			case "reappeared sealed":if err:=os.WriteFile(sealed,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			case "changed retained sidecar":if err:=os.WriteFile(native,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "changed policy":if err:=os.WriteFile(filepath.Join(op,"sealed","policy","projection.json"),[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "operator edit":if err:=os.WriteFile(final,[]byte("operator changed"),0600);err!=nil{t.Fatal(err)}
			case "operator deletion":if err:=os.Remove(final);err!=nil{t.Fatal(err)}
			case "retained capacity":a.maxBytes=grant.MaxBytes
			}
			records,closed,reserved,err:=a.inventoryWithClosure(ctx)
			bad:=mode=="damaged closure"||mode=="reappeared sealed"||mode=="changed retained sidecar"||mode=="changed policy"
			if (err!=nil)!=bad{t.Fatal("inventory",records,closed,reserved,err)}
			if bad{
				if _,err:=a.Allocate(ctx,second);err==nil{t.Fatal("damaged closure released capacity")}
				return
			}
			if mode=="lost closure"{
				if len(closed)!=0||reserved!=grant.MaxBytes{t.Fatal("missing closure discounted reservation",closed,reserved)}
				if _,err:=a.Allocate(ctx,second);!errors.Is(err,ErrStageLimit){t.Fatal("lost closure released capacity",err)}
				recovered,err:=a.closeRetiredOperation(ctx,grant,g,naming,extras,auth)
				if err!=nil||recovered!=observed{t.Fatal("lost closure recovery",err)}
			}else if len(records)!=1||!closed[grant.Request.OperationID]||reserved!=uint64(f.SidecarSize){t.Fatal("wrong retained charge",records,closed,reserved)}
			if mode=="operator edit"||mode=="operator deletion"{
				if _,err:=a.observeClosedOperation(ctx,grant,g,naming,extras,auth);err==nil{t.Fatal("historical closure mistaken for current receipt")}
			}
			next,err:=a.Allocate(ctx,second)
			if mode=="retained capacity"{if !errors.Is(err,ErrStageLimit){t.Fatal("retained bytes ignored",err)};return}
			if err!=nil||next.TransferID==grant.TransferID{t.Fatal("active capacity not released",next,err)}
			if _,err:=a.Allocate(ctx,grant.Request);!errors.Is(err,ErrStageRefused){t.Fatal("old operation identity reused",err)}
			_,closed,reserved,err=a.inventoryWithClosure(ctx)
			if err!=nil||len(closed)!=1||reserved!=next.MaxBytes+uint64(f.SidecarSize){t.Fatal("wrong combined accounting",closed,reserved,err)}
			checkBytes(native,nativeBytes)
		})
	}
}
