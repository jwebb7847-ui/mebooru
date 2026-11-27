package library

import (
	"bytes"
	"context"
	"io/fs"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
)

func TestReceivedNamingPreviewMatchesLegacyArrival(t *testing.T){
	for _,mode:=range []string{"plain","default folder","blank request","explicit folder","template folder","upload collision",
		"rename collision","folder collision","empty token","local md5","missing md5","invalid template",
		"escaping folder","stale path row","case only","extension mismatch","directory file","missing media","marked missing"}{
		t.Run(mode,func(t *testing.T){
			ctx:=context.Background()
			var buf bytes.Buffer
			if err:=png.Encode(&buf,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
			req:=gallery.ReceivedNamingRequest{Gallery:"A",ReceivedName:"original.png",Source:"derpibooru",PostID:"42"}
			var occupied string
			switch mode{
			case "default folder":req.DefaultFolder="incoming"
			case "blank request":req.DefaultFolder="incoming";req.RequestFolder="  "
			case "explicit folder":
				req.RequestFolder="literal-{source}";req.DefaultFolder="{gallery}";req.DefaultName="{source}-{post_id}"
			case "template folder":
				req.DefaultFolder="{source}/{gallery}";req.DefaultName="{name}-{post_id}"
			case "upload collision":
				occupied="original.png";req.DefaultName="{name}-{post_id}"
			case "rename collision":
				req.DefaultName="source-{post_id}";occupied="source-42.png"
			case "folder collision":
				req.DefaultFolder="{source}";occupied="derpibooru/original.png"
			case "empty token":
				req.Source="";req.DefaultName="{source}"
			case "local md5","missing md5":req.DefaultName="{md5}"
			case "invalid template":req.DefaultName="{artist}"
			case "escaping folder":req.RequestFolder="../outside"
			case "case only":req.DefaultName="ORIGINAL"
			case "extension mismatch":req.ReceivedName="original.jpg"
			case "directory file":req.RequestFolder="blocked/sub";occupied="blocked"
			}
			open:=func()*Gallery{
				t.Helper()
				dir:=t.TempDir()
				root:=filepath.Join(dir,"gallery")
				if err:=os.Mkdir(root,0700);err!=nil{t.Fatal(err)}
				g,err:=Open(config.Gallery{Name:"A",GalleryPath:root,DBPath:filepath.Join(dir,"gallery.sqlite"),ThumbnailsPath:filepath.Join(dir,"thumbs")})
				if err!=nil{t.Fatal(err)}
				t.Cleanup(func(){g.Close()})
				if occupied!=""{
					path:=filepath.Join(g.GalleryPath,filepath.FromSlash(occupied))
					if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil{t.Fatal(err)}
					if err:=os.WriteFile(path,[]byte("operator-owned"),0600);err!=nil{t.Fatal(err)}
				}
				return g
			}
			g:=open()
			diag:=filepath.Join(g.GalleryPath,"managed-diagnostic.png")
			if err:=os.WriteFile(diag,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
			landed,err:=g.LandInPlace(ctx,diag,LandSpec{Origin:"fixture"})
			if err!=nil||landed.Image==nil||landed.Duplicate{t.Fatal("fixture land",err)}
			id:=landed.Image.ID
			if mode=="missing media"{if err:=os.Remove(diag);err!=nil{t.Fatal(err)}}
			if mode=="marked missing"{if _,err:=gallery.SetMissing(g.DB,id,true);err!=nil{t.Fatal(err)}}
			if mode=="missing md5"{
				if _,err:=g.DB.Write.Exec(`UPDATE images SET md5='' WHERE id=?`,id);err!=nil{t.Fatal(err)}
			}
			if mode=="stale path row"{
				if _,err:=g.DB.Write.Exec(`INSERT INTO image_paths(image_id,path,is_canonical) VALUES(?,?,0)`,id,filepath.Join(g.GalleryPath,"original.png"));err!=nil{t.Fatal(err)}
			}
			inventory:=func()[]string{
				t.Helper()
				var entries []string
				err:=filepath.WalkDir(g.GalleryPath,func(path string,d fs.DirEntry,err error)error{
					if err!=nil{return err}
					entries=append(entries,path)
					return nil
				})
				if err!=nil{t.Fatal(err)}
				return entries
			}
			before:=inventory()
			var beforePath,beforeMD5 string
			var beforeRows int
			if err:=g.DB.Read.QueryRow(`SELECT canonical_path,md5 FROM images WHERE id=?`,id).Scan(&beforePath,&beforeMD5);err!=nil{t.Fatal(err)}
			if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_paths WHERE image_id=?`,id).Scan(&beforeRows);err!=nil{t.Fatal(err)}
			preview,err:=gallery.PreviewReceivedNaming(ctx,g.DB,g.Boundary(),id,req)
			bad:=mode=="missing md5"||mode=="invalid template"||mode=="escaping folder"||mode=="stale path row"||mode=="case only"||mode=="extension mismatch"||mode=="directory file"||mode=="missing media"||mode=="marked missing"
			if (err!=nil)!=bad{t.Fatalf("unexpected preview %+v %v",preview,err)}
			var afterPath,afterMD5 string
			var afterRows int
			if e:=g.DB.Read.QueryRow(`SELECT canonical_path,md5 FROM images WHERE id=?`,id).Scan(&afterPath,&afterMD5);e!=nil{t.Fatal(e)}
			if e:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM image_paths WHERE image_id=?`,id).Scan(&afterRows);e!=nil{t.Fatal(e)}
			if beforePath!=afterPath||beforeMD5!=afterMD5||beforeRows!=afterRows||!reflect.DeepEqual(before,inventory()){t.Fatal("preview changed gallery")}
			if bad{return}
			legacy:=open()
			folder,naming:=gallery.ReceivedNaming(req.Gallery,strings.TrimSpace(req.RequestFolder),req.DefaultFolder,req.DefaultName)
			dest,err:=legacy.Boundary().ResolveSubdir(folder)
			if err!=nil{t.Fatal(err)}
			res,err:=legacy.Land(ctx,dest,gallery.SanitizeReceivedName(req.ReceivedName),FromReader(bytes.NewReader(buf.Bytes())),
				LandSpec{Origin:"fixture",Naming:naming,NameSource:req.Source,NamePostID:req.PostID})
			if err!=nil||res.Image==nil||res.Duplicate{t.Fatal("legacy land",err)}
			var actual string
			if err:=legacy.DB.Read.QueryRow(`SELECT canonical_path FROM images WHERE id=?`,res.Image.ID).Scan(&actual);err!=nil{t.Fatal(err)}
			rel:=func(root,path string)string{
				t.Helper()
				v,err:=filepath.Rel(root,path)
				if err!=nil{t.Fatal(err)}
				return filepath.ToSlash(v)
			}
			if rel(g.GalleryPath,preview.InitialPath)!=rel(legacy.GalleryPath,res.Path)||
				rel(g.GalleryPath,preview.FinalPath)!=rel(legacy.GalleryPath,actual){
				t.Fatalf("preview %+v; legacy initial %s final %s",preview,res.Path,actual)
			}
			if preview.InitialSuffixed!=(mode=="upload collision")||preview.FinalSuffixed!=(mode=="rename collision"||mode=="folder collision"){t.Fatal("wrong suffix disposition",preview)}
			if occupied!=""{
				for _,x:=range []*Gallery{g,legacy}{
					got,err:=os.ReadFile(filepath.Join(x.GalleryPath,filepath.FromSlash(occupied)))
					if err!=nil||string(got)!="operator-owned"{t.Fatal("operator file changed",err)}
				}
			}
		})
	}
}
