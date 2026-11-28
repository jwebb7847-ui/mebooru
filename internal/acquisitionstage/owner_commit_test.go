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
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/jobs"
	"github.com/monbooru/monbooru/internal/library"
)

type ownerFixtureHost struct{cfg config.Gallery}
func(h ownerFixtureHost) Layout()library.Layout{return library.Layout{Galleries:[]config.Gallery{h.cfg},DataPath:filepath.Dir(h.cfg.DBPath)}}
func(h ownerFixtureHost) Background(string)library.Background{return library.Background{}}
func(h ownerFixtureHost) DefaultGallery()string{return h.cfg.Name}
func(h ownerFixtureHost) Added(config.Gallery)error{return ErrStageRefused}
func(h ownerFixtureHost) Removed(string)error{return ErrStageRefused}
func(h ownerFixtureHost) Repointed(string,string)error{return ErrStageRefused}
func(h ownerFixtureHost) Renamed(string,string,string,string)error{return ErrStageRefused}

// Exercise the same single local owner entry point, with the real Set
// fence and job reservation, rather than manually sequencing phases.
func TestGuardedOwnerCommit(t *testing.T){
	for _,mode:=range []string{"new png","held jpeg","restore webp","held gif","restore jpeg","same site refused","busy owner","revoked owner"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var b bytes.Buffer
			ext:="png"
			switch{
			case strings.HasSuffix(mode,"jpeg"):
				ext="jpg";if err:=jpeg.Encode(&b,image.NewRGBA(image.Rect(0,0,16,16)),nil);err!=nil{t.Fatal(err)}
			case strings.HasSuffix(mode,"gif"):
				ext="gif";if err:=gif.Encode(&b,image.NewPaletted(image.Rect(0,0,16,16),color.Palette{color.Black,color.White}),nil);err!=nil{t.Fatal(err)}
			case strings.HasSuffix(mode,"webp"):
				ext="webp";raw,err:=base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvD8ADAAdQvEIVuf+BiOh/AAA=");if err!=nil{t.Fatal(err)};b.Write(raw)
			default:
				if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			}
			a,stage,s,grant,_,f:=publicationFixtureData(t,ext,b.Bytes())
			defer a.Close()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42."+ext,SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			cfg:=config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")}
			jm:=jobs.NewManager()
			set,err:=library.OpenSet(ownerFixtureHost{cfg},jm,[]config.Gallery{cfg},cfg.Name)
			if err!=nil{t.Fatal(err)}
			defer set.Close()
			g:=set.Get(cfg.Name)
			kind:=OwnerDispositionNew
			old:=filepath.Join(s.GalleryRoot,"old."+ext)
			if strings.HasPrefix(mode,"held")||strings.HasPrefix(mode,"restore")||mode=="same site refused"{
				kind=OwnerDispositionHeld
				if err:=os.WriteFile(old,b.Bytes(),0600);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(ctx,old,library.LandSpec{Origin:"fixture"})
				if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("fixture",err)}
				site:="other";if mode=="same site refused"{site=row.SourceLabel}
				if _,_,err:=gallery.MergeSource(g.DB,g.TagSvc,res.Image.ID,site,"7","https://example.invalid/7","","",gallery.PostFile{},[]string{"rating:explicit","prior"},gallery.Withheld{});err!=nil{t.Fatal(err)}
				if strings.HasPrefix(mode,"restore"){
					kind=OwnerDispositionRestore
					if err:=os.Remove(old);err!=nil{t.Fatal(err)}
					if _,err:=gallery.SetMissing(g.DB,res.Image.ID,true);err!=nil{t.Fatal(err)}
				}
			}
			auth:=OwnerCommit{
				Current:func(context.Context,workerproto.StageAllocation)error{if mode=="revoked owner"{return ErrStageRefused};return nil},
				Disposition:func(context.Context,workerproto.StageAllocation)(string,error){return kind,nil},
				Naming:func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){return gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:f.RelativeName,Source:row.SourceLabel,PostID:row.PostID,DefaultName:"{source}-{post_id}"},nil},
				Extras:func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error){return contract.JobExtras{},nil},
				Cleanup:func(context.Context,workerproto.StageAllocation,OwnerReceipt)error{return nil},
			}
			if mode=="busy owner"{lane,err:=jm.Reserve();if err!=nil{t.Fatal(err)};defer lane.Release()}
			w,err:=a.CommitSealedForOwner(ctx,grant,set,auth)
			refused:=mode=="same site refused"||mode=="busy owner"||mode=="revoked owner"
			if (err!=nil)!=refused{t.Fatal("commit",w,err)}
			if refused{
				if _,err:=os.Stat(filepath.Join(s.GalleryRoot,finalName(grant,f)));!os.IsNotExist(err){t.Fatal("refused commit linked media",err)}
				return
			}
			if !validDigest(w.ReceiptSHA256)||w.Row.SHA256!=f.SHA256{t.Fatal("receipt",w)}
			got,err:=os.ReadFile(filepath.Join(s.GalleryRoot,w.Row.Name))
			if err!=nil||!bytes.Equal(got,b.Bytes()){t.Fatal("final bytes",err)}
			_,closed,_,err:=a.inventoryWithClosure(ctx)
			if err!=nil||!closed[grant.Request.OperationID]{t.Fatal("closure",err)}
			if _,err:=a.CommitSealedForOwner(ctx,grant,set,auth);err==nil{t.Fatal("replayed closed operation")}
		})
	}
}
