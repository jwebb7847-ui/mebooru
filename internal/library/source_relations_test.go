package library

import(
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/gallery"
)

func TestSourceParentLinkPreservesExistingRelationshipPolicy(t *testing.T){
	for _,mode:=range []string{"parent first","child first","manual source","self","missing parent"}{
		t.Run(mode,func(t *testing.T){
			dir:=t.TempDir()
			root:=filepath.Join(dir,"gallery")
			if err:=os.Mkdir(root,0700);err!=nil{t.Fatal(err)}
			g,err:=Open(config.Gallery{Name:"A",GalleryPath:root,DBPath:filepath.Join(dir,"gallery.sqlite"),ThumbnailsPath:filepath.Join(dir,"thumbs")})
			if err!=nil{t.Fatal(err)}
			defer g.Close()
			land:=func(name string,c uint8)int64{
				t.Helper()
				var b bytes.Buffer
				pic:=image.NewRGBA(image.Rect(0,0,16,16))
				pic.Set(4,4,color.RGBA{R:c,A:255})
				if err:=png.Encode(&b,pic);err!=nil{t.Fatal(err)}
				p:=filepath.Join(root,name+".png")
				if err:=os.WriteFile(p,b.Bytes(),0600);err!=nil{t.Fatal(err)}
				res,err:=g.LandInPlace(context.Background(),p,LandSpec{Origin:"fixture"})
				if err!=nil||res.Duplicate{t.Fatal("land",err)}
				return res.Image.ID
			}
			source:=func(id int64,post,url,parent string){
				t.Helper()
				if err:=gallery.ApplyCreateProvenance(g.DB,id,"derpibooru",post,url,"",parent,"","","","",gallery.PostFile{},nil);err!=nil{t.Fatal(err)}
			}
			parent,child,manual:=land("parent",1),land("child",2),land("manual",3)
			parentURL,childURL:="https://example.invalid/parent","https://example.invalid/child"
			switch mode{
			case "parent first":
				source(parent,"1",parentURL,"")
				g.LinkParentRelations(parent,parentURL,"")
				source(child,"2",childURL,parentURL)
				g.LinkParentRelations(child,childURL,parentURL)
			case "child first":
				source(child,"2",childURL,parentURL)
				g.LinkParentRelations(child,childURL,parentURL)
				source(parent,"1",parentURL,"")
				g.LinkParentRelations(parent,parentURL,"")
			case "manual source":
				if err:=g.RelationsSvc.AddDerivativeEdge(manual,child);err!=nil{t.Fatal(err)}
				source(parent,"1",parentURL,"")
				source(child,"2",childURL,parentURL)
				g.LinkParentRelations(child,childURL,parentURL)
				g.LinkParentRelations(parent,parentURL,"")
			case "self":
				source(child,"2",childURL,childURL)
				g.LinkParentRelations(child,childURL,childURL)
			case "missing parent":
				source(child,"2",childURL,parentURL)
				g.LinkParentRelations(child,childURL,parentURL)
			}
			var count int
			if err:=g.DB.Read.QueryRow(`SELECT COUNT(*) FROM derivative_edges WHERE derivative_image_id=?`,child).Scan(&count);err!=nil{t.Fatal(err)}
			if mode=="self"||mode=="missing parent"{if count!=0{t.Fatal("invented parent")};return}
			want:=parent;if mode=="manual source"{want=manual}
			var got int64
			if err:=g.DB.Read.QueryRow(`SELECT source_image_id FROM derivative_edges WHERE derivative_image_id=?`,child).Scan(&got);err!=nil||count!=1||got!=want{t.Fatalf("edge count=%d source=%d expected=%d err=%v",count,got,want,err)}
		})
	}
}
