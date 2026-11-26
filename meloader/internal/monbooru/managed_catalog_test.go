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
