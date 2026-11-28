package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"os"
	"path/filepath"
	"sync"
	"strings"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/tags"
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
		return op==workerproto.GalleryList&&(name==""||name=="A")||(op==workerproto.CategoryList||op==workerproto.ResolveTags)&&name=="A"
	}
	serve:=h.managedCatalogHandler(allowed,nil,nil)
	body,fault:=serve(ctx,workerproto.GalleryList,nil)
	if fault!=nil{t.Fatal(fault)}
	cat,err:=workerproto.DecodeGalleryCatalog(body)
	if err!=nil||len(cat.Galleries)!=1||cat.Galleries[0].Name!="A"||!cat.Galleries[0].Active{t.Fatalf("catalog: %+v %v",cat,err)}
	body,fault=serve(ctx,workerproto.CategoryList,json.RawMessage(`{"gallery":"A"}`))
	if fault!=nil{t.Fatal(fault)}
	cats,err:=workerproto.DecodeCategoryCatalog(body)
	if err!=nil||len(cats.Names)!=1||cats.Names[0]!="custom_a"{t.Fatalf("categories: %+v %v",cats,err)}
	// Real gallery rows: alias and implication lookup, missing name,
	// invalid rating, repeated input. No new tag should be written.
	catID,ok,err:=tags.CategoryIDByName(galleries[0].DB,"general")
	if err!=nil||!ok{t.Fatalf("general category: %v %v",ok,err)}
	canonical,err:=galleries[0].TagSvc.GetOrCreateTag("rainbow_dash",catID)
	if err!=nil{t.Fatal(err)}
	implied,err:=galleries[0].TagSvc.GetOrCreateTag("pony",catID)
	if err!=nil{t.Fatal(err)}
	if err:=galleries[0].TagSvc.RenameTagKeepAlias(canonical.ID,"rainbow_dash_canonical");err!=nil{t.Fatal(err)}
	if _,err:=galleries[0].TagSvc.AddImplication(canonical.ID,implied.ID);err!=nil{t.Fatal(err)}
	if _,err:=galleries[0].TagSvc.RejectTagName("blocked",catID);err!=nil{t.Fatal(err)}
	q:=workerproto.ResolveTagsRequest{Gallery:"A",Names:[]string{"rainbow_dash","rainbow_dash_canonical","never_created","rating:invalid","rainbow_dash","blocked"}}
	request,err:=workerproto.EncodeResolveTagsRequest(q)
	if err!=nil{t.Fatal(err)}
	body,fault=serve(ctx,workerproto.ResolveTags,request)
	if fault!=nil{t.Fatal(fault)}
	resolved,err:=workerproto.DecodeResolveTagsResponse(body,q)
	if err!=nil{t.Fatal(err)}
	if resolved.Results[0].Status!="alias"||resolved.Results[0].ResolvesTo!="rainbow_dash_canonical"||
		len(resolved.Results[0].Implies)!=1||resolved.Results[0].Implies[0]!="pony"||
		resolved.Results[1].Status!="exists"||resolved.Results[2].Status!="new"||
		resolved.Results[3].Status!="refused"||resolved.Results[4].Status!="alias"||resolved.Results[5].Status!="rejected"{
		t.Fatalf("resolution: %+v",resolved)
	}
	if _,err:=galleries[0].TagSvc.FindTag("never_created",catID);!errors.Is(err,tags.ErrTagNotFound){t.Fatalf("read mutated tags: %v",err)}
	// Compare actual compatibility handler output with managed DTO rows.
	legacyBody,_:=json.Marshal(map[string]any{"names":q.Names})
	w:=httptest.NewRecorder()
	h.resolveTags(w,httptest.NewRequest(http.MethodPost,"/api/v1/tags/resolve?gallery=A",strings.NewReader(string(legacyBody))))
	var legacy struct{Results []workerproto.TagResolution `json:"results"`}
	if w.Code!=http.StatusOK||json.Unmarshal(w.Body.Bytes(),&legacy)!=nil||!reflect.DeepEqual(legacy.Results,resolved.Results){
		t.Fatalf("legacy parity: %d %s",w.Code,w.Body.String())
	}
	// Permission revoked while acquiring gallery lifetime lock.
	checks:=0
	revoked:=h.managedCatalogHandler(func(context.Context,workerproto.Operation,string)bool{checks++;return checks==1},nil,nil)
	if _,f:=revoked(ctx,workerproto.ResolveTags,request);f==nil||f.Code!="forbidden"{t.Fatal("revoked authority accepted")}
	for _,tc:=range []struct{op workerproto.Operation;body string;code string}{
		{workerproto.CategoryList,`{"gallery":"B"}`,"forbidden"},
		{workerproto.ResolveTags,`{"gallery":"B","names":["rainbow_dash"]}`,"forbidden"},
		{workerproto.ResolveTags,`{"gallery":"","names":["rainbow_dash"]}`,"invalid"},
		{workerproto.ResolveTags,`{"gallery":"A","names":[null]}`,"invalid"},
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
	if _,f:=h.managedCatalogHandler(func(context.Context,workerproto.Operation,string)bool{return true},nil,nil)(ctx,workerproto.GalleryList,nil);f==nil||f.Code!="unavailable"{t.Fatal("partial managed discovery accepted")}
}

func TestManagedCatalogScopePinsGalleryAndOperation(t *testing.T){
	root:=t.TempDir()
	open:=func(dir string)*library.Gallery{
		t.Helper()
		path:=filepath.Join(root,dir)
		if err:=os.Mkdir(path,0700);err!=nil{t.Fatal(err)}
		g,err:=library.Open(config.Gallery{Name:"A",GalleryPath:path,DBPath:filepath.Join(path,"db.sqlite"),ThumbnailsPath:filepath.Join(path,"thumb")})
		if err!=nil{t.Fatal(err)}
		t.Cleanup(g.Close)
		return g
	}
	original:=open("original")
	replacement:=open("replacement")
	current:=original
	cfg:=config.Default()
	cfg.Galleries=[]config.Gallery{{Name:"A"}}
	var mu sync.RWMutex
	h:=New(func()*config.Config{return cfg},nil,func(name string)(*library.Gallery,bool){
		if name==""||name=="A"{return current,true}
		return nil,false
	},"test").WithGalleryLock(func()func(){mu.RLock();return mu.RUnlock})
	ctx:=context.Background()
	grants:=map[string][]workerproto.Operation{"A":{workerproto.GalleryList,workerproto.ResolveTags}}
	scope,err:=h.NewCatalogScope(ctx,true,grants)
	if err!=nil{t.Fatal(err)}
	// Caller mutation cannot widen the snapshot.
	grants["A"][1]=workerproto.CategoryList
	serve:=h.ManagedCatalogHandler(scope)
	q:=json.RawMessage(`{"gallery":"A","names":["new_tag"]}`)
	if _,f:=serve(ctx,workerproto.ResolveTags,q);f!=nil{t.Fatal(f)}
	if _,f:=serve(ctx,workerproto.CategoryList,json.RawMessage(`{"gallery":"A"}`));f==nil||f.Code!="forbidden"{t.Fatal("scope widened")}
	if _,f:=serve(ctx,workerproto.GalleryList,nil);f!=nil{t.Fatal(f)}
	// Config removal cannot be masked by the still-live resolver.
	mu.Lock()
	cfg.Galleries=nil
	mu.Unlock()
	for _,op:=range []workerproto.Operation{workerproto.GalleryList,workerproto.ResolveTags}{
		var p json.RawMessage
		if op==workerproto.ResolveTags{p=q}
		if body,f:=serve(ctx,op,p);body!=nil||f==nil||f.Code!="forbidden"{t.Fatalf("unconfigured grant: %s %s %+v",op,body,f)}
	}
	if _,err:=h.NewCatalogScope(ctx,true,map[string][]workerproto.Operation{"A":{workerproto.GalleryList}});err==nil{t.Fatal("unconfigured resolver granted scope")}
	mu.Lock()
	cfg.Galleries=[]config.Gallery{{Name:"A"}}
	mu.Unlock()
	// Both galleries remain open: pointer identity, not just Closed,
	// must prevent a matching name from inheriting an old grant.
	mu.Lock()
	current=replacement
	mu.Unlock()
	for _,op:=range []workerproto.Operation{workerproto.GalleryList,workerproto.ResolveTags}{
		var p json.RawMessage
		if op==workerproto.ResolveTags{p=q}
		if body,f:=serve(ctx,op,p);body!=nil||f==nil||f.Code!="forbidden"{t.Fatalf("stale grant: %s %s %+v",op,body,f)}
	}
	fresh,err:=h.NewCatalogScope(ctx,false,map[string][]workerproto.Operation{"A":{workerproto.ResolveTags}})
	if err!=nil{t.Fatal(err)}
	if _,f:=h.ManagedCatalogHandler(fresh)(ctx,workerproto.ResolveTags,q);f!=nil{t.Fatal(f)}
	fresh.Revoke()
	if _,f:=h.ManagedCatalogHandler(fresh)(ctx,workerproto.ResolveTags,q);f==nil||f.Code!="forbidden"{t.Fatal("revocation ignored")}
	if _,err:=h.NewCatalogScope(ctx,false,map[string][]workerproto.Operation{"A":{workerproto.ImportMedia}});err==nil{t.Fatal("mutation grant accepted")}
	if _,err:=h.NewCatalogScope(ctx,false,map[string][]workerproto.Operation{"":{workerproto.ResolveTags}});err==nil{t.Fatal("implicit gallery grant accepted")}
	if _,err:=h.NewCatalogScope(ctx,false,map[string][]workerproto.Operation{"A":{workerproto.GalleryList}});err==nil{t.Fatal("implicit discovery grant accepted")}
}


// Actual framed private session, not direct handler invocation. This
// is isolated; no production route, worker launch or network is enabled.
func TestManagedCatalogPrivateSessionAuthorization(t *testing.T){
	ctx,cancel:=context.WithTimeout(context.Background(),10*time.Second)
	defer cancel()
	dir:=t.TempDir()
	gc:=config.Gallery{Name:"A",GalleryPath:dir,DBPath:filepath.Join(dir,"db.sqlite"),ThumbnailsPath:filepath.Join(dir,"thumb")}
	g,err:=library.Open(gc)
	if err!=nil{t.Fatal(err)}
	defer g.Close()
	cfg:=config.Default()
	cfg.Galleries=[]config.Gallery{gc}
	var mu sync.RWMutex
	h:=New(func()*config.Config{return cfg},nil,func(name string)(*library.Gallery,bool){
		if name==""||name=="A"{return g,true};return nil,false
	},"test").WithGalleryLock(func()func(){mu.RLock();return mu.RUnlock})
	scope,err:=h.NewCatalogScope(ctx,true,map[string][]workerproto.Operation{"A":{workerproto.GalleryList,workerproto.ResolveTags}})
	if err!=nil{t.Fatal(err)}
	lr,ww,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer lr.Close();defer ww.Close()
	wr,lw,err:=os.Pipe()
	if err!=nil{t.Fatal(err)}
	defer wr.Close();defer lw.Close()
	type sessionResult struct{s *workerproto.Session;err error}
	ready:=make(chan sessionResult,1)
	go func(){s,err:=workerproto.OpenSession(ctx,wr,ww,workerproto.Worker,nil);ready<-sessionResult{s,err}}()
	lib,err:=workerproto.OpenSession(ctx,lr,lw,workerproto.Library,h.ManagedCatalogHandler(scope))
	if err!=nil{t.Fatal(err)}
	defer lib.Close()
	r:=<-ready
	if r.err!=nil{t.Fatal(r.err)}
	w:=r.s
	defer w.Close()
	request,err:=workerproto.EncodeResolveTagsRequest(workerproto.ResolveTagsRequest{Gallery:"A",Names:[]string{"fixture"}})
	if err!=nil{t.Fatal(err)}
	e,err:=w.Call(ctx,workerproto.ResolveTags,request)
	if err!=nil||e.Error!=nil{t.Fatalf("scoped read: %+v %v",e,err)}
	for _,tc:=range []struct{op workerproto.Operation;body json.RawMessage;code string}{
		{workerproto.CategoryList,json.RawMessage(`{"gallery":"A"}`),"forbidden"},
		{workerproto.ResolveTags,json.RawMessage(`{"gallery":"B","names":["fixture"]}`),"forbidden"},
		{workerproto.ResolveTags,json.RawMessage(`{"gallery":"A","names":["fixture"],"path":"/tmp"}`),"invalid"},
		{workerproto.ImportMedia,json.RawMessage(`{}`),"unsupported"},
	}{
		e,err:=w.Call(ctx,tc.op,tc.body)
		if err!=nil||e.Error==nil||e.Error.Code!=tc.code||len(e.Payload)!=0{t.Fatalf("refusal %s: %+v %v",tc.op,e,err)}
	}
	if _,err:=w.Call(ctx,workerproto.OperationStrictTransferStart,nil);!errors.Is(err,workerproto.ErrDirection){t.Fatalf("custody direction: %v",err)}
	scope.Revoke()
	e,err=w.Call(ctx,workerproto.ResolveTags,request)
	if err!=nil||e.Error==nil||e.Error.Code!="forbidden"{t.Fatalf("revoked scope: %+v %v",e,err)}
}
