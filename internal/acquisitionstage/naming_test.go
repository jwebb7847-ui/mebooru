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
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

func TestManagedNamingRelocation(t *testing.T){
	for _,mode:=range []string{"plain","template","upload collision","rename collision","nested",
		"missing directory","wrong received name","terminal already","revoked after attempt",
		"collision after attempt","revoked after link","DB write failure","revoked after DB",
		"lost result","damaged result","changed file","same bytes replacement","changed DB","changed settings","old path returns","extra link"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			a,stage,s,grant,auth,f:=publicationFixtureData(t,"png",buf.Bytes())
			defer func(){if err:=a.Close();err!=nil{t.Error(err)}}()
			row:=acquisitionplan.ApprovedPolicyRow{PostID:f.ID,NativeSHA256:strings.Repeat("a",64),MediaURL:"https://example.invalid/42.png",
				SourceLabel:"derpibooru",PostURL:"https://derpibooru.org/images/42",Rating:"general",
				Tags:[]string{"fixture","rating:general"},Provenance:&acquisitionplan.ApprovedProvenance{}}
			body,hash,err:=acquisitionplan.EncodeApprovedPolicy(grant.Request.Transfer,[]acquisitionplan.ApprovedPolicyRow{row})
			if err!=nil{t.Fatal(err)}
			if err:=a.BindPolicy(ctx,grant,func(context.Context,workerproto.StageAllocation)([]byte,string,error){return body,hash,nil});err!=nil{t.Fatal(err)}
			g,err:=library.Open(config.Gallery{Name:"A",GalleryPath:s.GalleryRoot,DBPath:filepath.Join(filepath.Dir(stage),"gallery.sqlite"),ThumbnailsPath:filepath.Join(filepath.Dir(stage),"thumbs")})
			if err!=nil{t.Fatal(err)}
			defer g.Close()
			if err:=a.preparePublication(ctx,grant);err!=nil{t.Fatal(err)}
			if _,err:=a.publishPrepared(ctx,grant);err!=nil{t.Fatal(err)}
			if err:=a.landNewPrepared(ctx,grant,g);err!=nil{t.Fatal(err)}
			mapped,err:=a.applyMappedMetadata(ctx,grant,g)
			if err!=nil||len(mapped)!=1{t.Fatal("metadata",err)}
			if err:=a.applyRelationships(ctx,grant,g);err!=nil{t.Fatal(err)}
			id:=mapped[0].ImageID
			old:=filepath.Join(s.GalleryRoot,finalName(grant,f))
			private:=filepath.Join(stage,grant.Request.OperationID,"publication","media",f.RelativeName)
			dir:=filepath.Join(stage,grant.Request.OperationID,"publication")
			attempt:=filepath.Join(dir,"naming-attempt.json");result:=filepath.Join(dir,"naming-result.json")
			var pid int64
			if err:=g.DB.Read.QueryRow(`SELECT id FROM image_paths WHERE image_id=? AND is_canonical=1`,id).Scan(&pid);err!=nil{t.Fatal(err)}
			req:=gallery.ReceivedNamingRequest{Gallery:g.Name,ReceivedName:f.RelativeName,Source:row.SourceLabel,PostID:row.PostID}
			final:=filepath.Join(s.GalleryRoot,f.RelativeName)
			var occupied string
			switch mode{
			case "template":req.DefaultName="{source}-{post_id}";final=filepath.Join(s.GalleryRoot,"derpibooru-42.png")
			case "upload collision":
				occupied=final;final=filepath.Join(s.GalleryRoot,"42_1.png")
			case "rename collision":
				req.DefaultName="{source}-{post_id}";occupied=filepath.Join(s.GalleryRoot,"derpibooru-42.png")
				final=filepath.Join(s.GalleryRoot,"derpibooru-4201.png")
			case "nested","missing directory":
				req.DefaultFolder="{source}";final=filepath.Join(s.GalleryRoot,"derpibooru","42.png")
				if mode=="nested"{if err:=os.Mkdir(filepath.Dir(final),0700);err!=nil{t.Fatal(err)}}
			case "wrong received name":req.ReceivedName="foreign.png"
			}
			if occupied!=""{if err:=os.WriteFile(occupied,[]byte("operator-owned"),0600);err!=nil{t.Fatal(err)}}
			if mode=="terminal already"{if _,err:=a.finishNewReceipt(ctx,grant,g);err!=nil{t.Fatal(err)}}
			if mode=="DB write failure"{
				if _,err:=g.DB.Write.Exec(`CREATE TRIGGER refuse_move BEFORE UPDATE OF path ON image_paths BEGIN SELECT RAISE(ABORT,'fixture'); END`);err!=nil{t.Fatal(err)}
			}
			// Owner decisions can change at each independent authorization check.
			revoked:=false
			namingAuth:=func(context.Context,workerproto.StageAllocation)(gallery.ReceivedNamingRequest,error){
				switch mode{
				case "revoked after attempt":
					if _,err:=os.Stat(attempt);err==nil&&!revoked{revoked=true;return req,ErrStageRefused}
				case "collision after attempt":
					if _,err:=os.Stat(attempt);err==nil&&!revoked{
						revoked=true
						if err:=os.WriteFile(final,[]byte("late operator"),0600);err!=nil{t.Fatal(err)}
					}
				case "revoked after link":
					if _,err:=os.Stat(final);err==nil&&!revoked{revoked=true;return req,ErrStageRefused}
				case "revoked after DB":
					var current string
					if err:=g.DB.Read.QueryRow(`SELECT canonical_path FROM images WHERE id=?`,id).Scan(&current);err!=nil{t.Fatal(err)}
					if current==final&&!revoked{revoked=true;return req,ErrStageRefused}
				}
				return req,nil
			}
			err=a.applyNaming(ctx,grant,g,namingAuth)
			preRefusal:=mode=="missing directory"||mode=="wrong received name"||mode=="terminal already"
			partial:=mode=="revoked after attempt"||mode=="collision after attempt"||mode=="revoked after link"||mode=="DB write failure"||mode=="revoked after DB"
			if (err!=nil)!=(preRefusal||partial){t.Fatalf("unexpected relocation %v",err)}
			if preRefusal{
				if _,err:=os.Stat(attempt);!os.IsNotExist(err){t.Fatal("unexpected attempt",err)}
				if _,err:=os.Stat(old);err!=nil{t.Fatal("old lost",err)}
				return
			}
			if _,err:=os.Stat(attempt);err!=nil{t.Fatal("attempt missing",err)}
			if _,err:=a.finishNewReceipt(ctx,grant,g);err==nil{t.Fatal("restricted receipt bypassed naming")}
			checkBytes:=func(path string,want []byte){
				t.Helper();b,err:=os.ReadFile(path);if err!=nil||!bytes.Equal(b,want){t.Fatal("bytes",path,err)}
			}
			checkBytes(private,buf.Bytes())
			if occupied!=""{checkBytes(occupied,[]byte("operator-owned"))}
			var canon string
			var pathID int64
			if err:=g.DB.Read.QueryRow(`SELECT i.canonical_path,ip.id FROM images i JOIN image_paths ip ON i.id=ip.image_id WHERE i.id=? AND ip.is_canonical=1`,id).Scan(&canon,&pathID);err!=nil||pathID!=pid{t.Fatal("path identity",err)}
			if partial{
				checkBytes(old,buf.Bytes())
				if _,err:=os.Stat(result);!os.IsNotExist(err){t.Fatal("partial result",err)}
				if mode=="revoked after link"||mode=="DB write failure"||mode=="revoked after DB"{checkBytes(final,buf.Bytes())}
				if mode=="collision after attempt"{checkBytes(final,[]byte("late operator"))}
				if mode=="revoked after DB"{if canon!=final{t.Fatal("DB phase not reached")}}else if canon!=old{t.Fatal("unexpected DB mutation")}
			}else{
				checkBytes(final,buf.Bytes())
				if canon!=final{t.Fatal("wrong canonical",canon)}
				if _,err:=os.Stat(old);!os.IsNotExist(err){t.Fatal("old still present",err)}
			}
			if err:=a.Close();err!=nil{t.Fatal(err)}
			a,err=Open(stage,2,4096,auth);if err!=nil{t.Fatal(err)}
			if err:=a.applyNaming(ctx,grant,g,namingAuth);err==nil{t.Fatal("replayed move")}
			switch mode{
			case "lost result":if err:=os.Remove(result);err!=nil{t.Fatal(err)}
			case "damaged result":if err:=os.WriteFile(result,[]byte("{}"),0600);err!=nil{t.Fatal(err)}
			case "changed file":if err:=os.WriteFile(final,[]byte("changed"),0600);err!=nil{t.Fatal(err)}
			case "same bytes replacement":
				if err:=os.Remove(final);err!=nil{t.Fatal(err)}
				if err:=os.WriteFile(final,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			case "changed DB":if _,err:=g.DB.Write.Exec(`UPDATE images SET folder_path='wrong' WHERE id=?`,id);err!=nil{t.Fatal(err)}
			case "changed settings":req.DefaultName="changed"
			case "old path returns":if err:=os.WriteFile(old,[]byte("operator"),0600);err!=nil{t.Fatal(err)}
			case "extra link":if err:=os.Link(final,filepath.Join(s.GalleryRoot,"foreign.png"));err!=nil{t.Fatal(err)}
			}
			err=a.observeNaming(ctx,grant,g,namingAuth)
			bad:=partial||mode=="lost result"||mode=="damaged result"||mode=="changed file"||mode=="same bytes replacement"||
				mode=="changed DB"||mode=="changed settings"||mode=="old path returns"||mode=="extra link"
			if (err!=nil)!=bad{t.Fatal("unexpected recovery",err)}
			if mode=="old path returns"{checkBytes(old,[]byte("operator"))}
			if mode=="collision after attempt"{checkBytes(final,[]byte("late operator"))}
			if mode!="changed file"{checkBytes(private,buf.Bytes())}
			if _,err:=os.Stat(filepath.Join(stage,grant.Request.OperationID));errors.Is(err,os.ErrNotExist){t.Fatal("evidence removed")}
		})
	}
}
