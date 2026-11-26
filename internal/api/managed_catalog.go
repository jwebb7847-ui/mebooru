package api

import (
	"context"
	"encoding/json"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// CatalogAuthority is a library-owned session scope check. It must be
// nonblocking, must not reenter gallery locks, and cannot be derived
// from worker-supplied credentials. Gallery discovery requires both the
// empty-gallery permission and each returned gallery permission.
type CatalogAuthority func(context.Context,workerproto.Operation,string)bool

// ManagedCatalogHandler exposes only the two bounded read-only catalog
// callbacks. Requires an explicit trusted authority and the same gallery
// lifetime lock used by REST. Not mounted or attached in production yet.
func(h *Handler)ManagedCatalogHandler(allow CatalogAuthority)workerproto.Handler{
	return func(ctx context.Context,op workerproto.Operation,body json.RawMessage)(json.RawMessage,*workerproto.Fault){
		fault:=func(code string)(json.RawMessage,*workerproto.Fault){return nil,&workerproto.Fault{Code:code}}
		if ctx.Err()!=nil{return fault("canceled")}
		if op!=workerproto.GalleryList&&op!=workerproto.CategoryList{return fault("unsupported")}
		if allow==nil||h.lock==nil||h.cfg==nil||h.resolver==nil{return fault("forbidden")}
		name:=""
		switch op{
		case workerproto.GalleryList:
			if len(body)!=0{return fault("invalid")}
		case workerproto.CategoryList:
			q,err:=workerproto.DecodeCategoryCatalogRequest(body)
			if err!=nil{return fault("invalid")}
			name=q.Gallery
		}
		if !allow(ctx,op,name){return fault("forbidden")}
		unlock:=h.lock()
		defer unlock()
		if ctx.Err()!=nil{return fault("canceled")}
		if !allow(ctx,op,name){return fault("forbidden")}
		var payload json.RawMessage
		var err error
		if op==workerproto.GalleryList{
			list,listErr:=h.galleryCatalog(func(n string)bool{return allow(ctx,op,n)},true)
			if listErr!=nil{return fault("unavailable")}
			for _,g:=range list{if !allow(ctx,op,g.Name){return fault("forbidden")}}
			payload,err=workerproto.EncodeGalleryCatalog(workerproto.GalleryCatalog{Galleries:list})
		}else{
			g,ok:=h.resolver(name)
			if !ok||g==nil||g.Closed()||g.Name!=name{return fault("invalid_gallery")}
			cats,listErr:=g.TagSvc.ListCategories()
			if listErr!=nil{return fault("unavailable")}
			names:=make([]string,0,len(cats))
			for _,c:=range cats{if !c.IsBuiltin{names=append(names,c.Name)}}
			payload,err=workerproto.EncodeCategoryCatalog(workerproto.CategoryCatalog{Names:names})
		}
		if ctx.Err()!=nil{return fault("canceled")}
		if !allow(ctx,op,name){return fault("forbidden")}
		if err!=nil{return fault("unavailable")}
		return payload,nil
	}
}
