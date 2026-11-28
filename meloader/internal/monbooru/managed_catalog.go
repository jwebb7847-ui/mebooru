package monbooru

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// CatalogSession is restricted to the private typed transport. No HTTP
// client or address is available as a fallback after a failed call.
type CatalogSession interface {
	Call(context.Context,workerproto.Operation,json.RawMessage)(workerproto.Envelope,error)
}

type ManagedCatalogClient struct{session CatalogSession}

func NewManagedCatalogClient(session CatalogSession)*ManagedCatalogClient{
	return &ManagedCatalogClient{session:session}
}

func(c *ManagedCatalogClient)call(ctx context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,error){
	if c==nil||c.session==nil{return nil,errors.New("managed catalog session unavailable")}
	e,err:=c.session.Call(ctx,op,body)
	if err!=nil{return nil,err}
	if e.Error!=nil{return nil,fmt.Errorf("managed catalog refused: %s",e.Error.Code)}
	return e.Payload,nil
}

func(c *ManagedCatalogClient)ListGalleries(ctx context.Context)([]Gallery,error){
	body,err:=c.call(ctx,workerproto.GalleryList,nil)
	if err!=nil{return nil,err}
	result,err:=workerproto.DecodeGalleryCatalog(body)
	return result.Galleries,err
}

func(c *ManagedCatalogClient)galleryCategories(ctx context.Context,name string)([]string,error){
	body,err:=json.Marshal(workerproto.CategoryCatalogRequest{Gallery:name})
	if err!=nil{return nil,err}
	if _,err:=workerproto.DecodeCategoryCatalogRequest(body);err!=nil{return nil,err}
	body,err=c.call(ctx,workerproto.CategoryList,body)
	if err!=nil{return nil,err}
	result,err:=workerproto.DecodeCategoryCatalog(body)
	return result.Names,err
}

func(c *ManagedCatalogClient)CustomCategories(ctx context.Context)([]string,bool,error){
	return customCategories(ctx,c.ListGalleries,c.galleryCategories)
}

// ResolveTags uses the same read-only library behavior as the legacy HTTP
// client, but requires an explicit gallery and verifies every returned row.
func(c *ManagedCatalogClient)ResolveTags(ctx context.Context,gallery string,names []string)([]ResolvedTag,error){
	q:=workerproto.ResolveTagsRequest{Gallery:gallery,Names:append([]string(nil),names...)}
	body,err:=workerproto.EncodeResolveTagsRequest(q)
	if err!=nil{return nil,err}
	body,err=c.call(ctx,workerproto.ResolveTags,body)
	if err!=nil{return nil,err}
	out,err:=workerproto.DecodeResolveTagsResponse(body,q)
	if err!=nil{return nil,err}
	results:=make([]ResolvedTag,0,len(out.Results))
	for _,r:=range out.Results{
		results=append(results,ResolvedTag{Name:r.Name,Status:r.Status,ResolvesTo:r.ResolvesTo,Implies:r.Implies,Reason:r.Reason})
	}
	return results,nil
}
