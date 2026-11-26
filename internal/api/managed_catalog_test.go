package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"strings"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/library"
)

func TestManagedCatalogHandlerActualGalleries(t *testing.T){
	root:=t.TempDir()
	cfg:=config.Default()
	var galleries []*library.Gallery
	for _,name:=range []string{"A","B"}{
		dir:=filepath.Join(root,name)
		if err:=os.Mkdir(dir,0700);err!=nil{t.Fatal(err)}
		gc:=config.Gallery{Name:name,GalleryPath:dir,DBPath:filepath.Join(dir,"db.sqlite"),ThumbnailsPath:filepath.Join(dir,"thumb")}
		g,err:=library.Open(gc)
		if err!=nil{t.Fatal(err)}
		t.Cleanup(g.Close)
		galleries=append(galleries,g)
		cfg.Galleries=append(cfg.Galleries,gc)
		if _,err:=g.TagSvc.CreateCategory("custom_"+strings.ToLower(name),"#888888");err!=nil{t.Fatal(err)}
	}
	// Default config might include a gallery; keep exactly the fixture.
	cfg.Galleries=cfg.Galleries[len(cfg.Galleries)-2:]
	resolver:=func(name string)(*library.Gallery,bool){
		if name==""{return galleries[0],true}
		for _,g:=range galleries{if g.Name==name{return g,true}}
		return nil,false
	}
	var mu sync.RWMutex
	h:=New(func()*config.Config{return cfg},nil,resolver,"test").WithGalleryLock(func()func(){mu.RLock();return mu.RUnlock})
	ctx:=context.Background()
	allowed:=func(_ context.Context,op workerproto.Operation,name string)bool{
		return op==workerproto.GalleryList&&(name==""||name=="A")||op==workerproto.CategoryList&&name=="A"
	}
	serve:=h.ManagedCatalogHandler(allowed)
	body,fault:=serve(ctx,workerproto.GalleryList,nil)
	if fault!=nil{t.Fatal(fault)}
	cat,err:=workerproto.DecodeGalleryCatalog(body)
	if err!=nil||len(cat.Galleries)!=1||cat.Galleries[0].Name!="A"||!cat.Galleries[0].Active{t.Fatalf("catalog: %+v %v",cat,err)}
	body,fault=serve(ctx,workerproto.CategoryList,json.RawMessage(`{"gallery":"A"}`))
	if fault!=nil{t.Fatal(fault)}
	cats,err:=workerproto.DecodeCategoryCatalog(body)
	if err!=nil||len(cats.Names)!=1||cats.Names[0]!="custom_a"{t.Fatalf("categories: %+v %v",cats,err)}
	for _,tc:=range []struct{op workerproto.Operation;body string;code string}{
		{workerproto.CategoryList,`{"gallery":"B"}`,"forbidden"},
		{workerproto.CategoryList,`{"gallery":""}`,"invalid"},
		{workerproto.CategoryList,`{"gallery":"A","path":"/x"}`,"invalid"},
		{workerproto.GalleryList,`{}`,"invalid"},
		{workerproto.ImportMedia,`{}`,"unsupported"},
	}{
		got,f:=serve(ctx,tc.op,json.RawMessage(tc.body))
		if got!=nil||f==nil||f.Code!=tc.code{t.Fatalf("refusal %v: %s %+v",tc,got,f)}
	}
	if _,f:=h.ManagedCatalogHandler(nil)(ctx,workerproto.GalleryList,nil);f==nil||f.Code!="forbidden"{t.Fatal("missing authority accepted")}
	cancelCtx,cancel:=context.WithCancel(ctx);cancel()
	if _,f:=serve(cancelCtx,workerproto.GalleryList,nil);f==nil||f.Code!="canceled"{t.Fatal("canceled read accepted")}
	// An authorized unavailable gallery must not silently disappear from
	// managed discovery, unlike the compatibility HTTP route.
	cfg.Galleries=append(cfg.Galleries,config.Gallery{Name:"missing"})
	if _,f:=h.ManagedCatalogHandler(func(context.Context,workerproto.Operation,string)bool{return true})(ctx,workerproto.GalleryList,nil);f==nil||f.Code!="unavailable"{t.Fatal("partial managed discovery accepted")}
}
