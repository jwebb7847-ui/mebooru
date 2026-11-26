package monbooru

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)
type catalogCall func(context.Context,workerproto.Operation,json.RawMessage)(workerproto.Envelope,error)
func(f catalogCall)Call(ctx context.Context,op workerproto.Operation,p json.RawMessage)(workerproto.Envelope,error){return f(ctx,op,p)}

func TestManagedCatalogClientUnionAndRefusal(t *testing.T){
	ctx:=context.Background()
	failed:=false
	c:=NewManagedCatalogClient(catalogCall(func(_ context.Context,op workerproto.Operation,p json.RawMessage)(workerproto.Envelope,error){
		switch op{
		case workerproto.GalleryList:
			if len(p)!=0{t.Fatal("unexpected discovery payload")}
			return workerproto.Envelope{Payload:json.RawMessage(`{"galleries":[{"name":"A","images":0,"tags":0,"active":true},{"name":"B","images":0,"tags":0,"active":false}]}`)},nil
		case workerproto.CategoryList:
			req,err:=workerproto.DecodeCategoryCatalogRequest(p)
			if err!=nil{t.Fatal(err)}
			if req.Gallery=="A"{return workerproto.Envelope{Payload:json.RawMessage(`{"names":["z","a"]}`)},nil}
			if req.Gallery!="B"{t.Fatalf("unexpected gallery %s",req.Gallery)}
			if failed{return workerproto.Envelope{Error:&workerproto.Fault{Code:"unavailable"}},nil}
			return workerproto.Envelope{Payload:json.RawMessage(`{"names":["a","b"]}`)},nil
		default:t.Fatalf("unexpected call: %s",op)
		}
		return workerproto.Envelope{},nil
	}))
	names,complete,err:=c.CustomCategories(ctx)
	if err!=nil||!complete||!slices.Equal(names,[]string{"a","b","z"}){t.Fatalf("union: %v %v %v",names,complete,err)}
	failed=true
	names,complete,err=c.CustomCategories(ctx)
	if err!=nil||complete||!slices.Equal(names,[]string{"a","z"}){t.Fatalf("partial: %v %v %v",names,complete,err)}
	bad:=NewManagedCatalogClient(catalogCall(func(context.Context,workerproto.Operation,json.RawMessage)(workerproto.Envelope,error){return workerproto.Envelope{Payload:json.RawMessage(`{"galleries":null}`)},nil}))
	if _,err:=bad.ListGalleries(ctx);err==nil{t.Fatal("malformed response accepted")}
	lost:=errors.New("session lost")
	down:=NewManagedCatalogClient(catalogCall(func(context.Context,workerproto.Operation,json.RawMessage)(workerproto.Envelope,error){return workerproto.Envelope{},lost}))
	if _,err:=down.ListGalleries(ctx);!errors.Is(err,lost){t.Fatalf("session failure: %v",err)}
}

func TestManagedTagResolutionClient(t *testing.T){
	ctx:=context.Background()
	calls:=0
	reply:=`{"gallery":"A","results":[{"name":"x","status":"alias","resolves_to":"y","implies":[]}]}`
	c:=NewManagedCatalogClient(catalogCall(func(_ context.Context,op workerproto.Operation,p json.RawMessage)(workerproto.Envelope,error){
		calls++
		q,err:=workerproto.DecodeResolveTagsRequest(p)
		if op!=workerproto.ResolveTags||err!=nil||q.Gallery!="A"||!slices.Equal(q.Names,[]string{"x"}){t.Fatalf("request: %s %+v %v",op,q,err)}
		return workerproto.Envelope{Payload:json.RawMessage(reply)},nil
	}))
	rows,err:=c.ResolveTags(ctx,"A",[]string{"x"})
	if err!=nil||len(rows)!=1||rows[0].ResolvesTo!="y"{t.Fatalf("result: %+v %v",rows,err)}
	if _,err:=c.ResolveTags(ctx,"",[]string{"x"});err==nil||calls!=1{t.Fatal("implicit gallery sent")}
	reply=`{"gallery":"B","results":[{"name":"x","status":"new","implies":[]}]}`
	if rows,err:=c.ResolveTags(ctx,"A",[]string{"x"});err==nil||rows!=nil{t.Fatal("wrong gallery accepted")}
	reply=`{"gallery":"A","results":[{"name":"z","status":"new","implies":[]}]}`
	if rows,err:=c.ResolveTags(ctx,"A",[]string{"x"});err==nil||rows!=nil{t.Fatal("wrong name accepted")}
	if calls!=3{t.Fatalf("unexpected call count: %d",calls)}
	lost:=errors.New("lost")
	down:=NewManagedCatalogClient(catalogCall(func(context.Context,workerproto.Operation,json.RawMessage)(workerproto.Envelope,error){return workerproto.Envelope{},lost}))
	if _,err:=down.ResolveTags(ctx,"A",[]string{"x"});!errors.Is(err,lost){t.Fatalf("session error: %v",err)}
}
