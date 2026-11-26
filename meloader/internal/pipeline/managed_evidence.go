package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/gdl"
	"github.com/monbooru/monloader/internal/kwdict"
	"github.com/monbooru/monloader/internal/mapping"
	"github.com/monbooru/monloader/internal/taxonomy"
)

var ErrManagedMetadataEvidence = errors.New("managed metadata evidence refused")

type ManagedEvidencePost struct {
	// Item contains unchanged native extractor metadata. Interpreted
	// carries source-confirmed taxonomy in a separate map.
	Item gdl.Item
	Interpreted map[string]any
}

// ManagedEvidenceWindow composes the native envelope, existing native
// tag/rating validation and scoped taxonomy. The grant, subcategory,
// bytes and opaque generation MUST come from trusted contained metadata
// completion, not a requester. It is a one-use in-process candidate;
// no durable receipt, production dispatcher or transfer verdict.
type ManagedEvidenceWindow struct{
	grant workerproto.MetadataPlanRequest
	posts []workerproto.MetadataPost
	taxonomy *taxonomy.ManagedTaxonomyWindow
	gate *workerproto.DrainGate
	generation workerproto.DrainGeneration
	scope taxonomy.ManagedTaxonomyScope
	mapper *mapping.Mapper
	used atomic.Bool
}

func NewManagedEvidenceWindow(grant workerproto.MetadataPlanRequest, subcategory string, body []byte,
	gate *workerproto.DrainGate,generation workerproto.DrainGeneration,scope taxonomy.ManagedTaxonomyScope,
	mapper *mapping.Mapper,transport http.RoundTripper)(*ManagedEvidenceWindow,error){
	if gate==nil||scope==nil||mapper==nil{return nil,ErrManagedMetadataEvidence}
	posts,err:=workerproto.DecodeMetadataEvidence(body,grant,subcategory)
	if err!=nil{return nil,ErrManagedMetadataEvidence}
	w:=&ManagedEvidenceWindow{grant:grant,posts:posts,gate:gate,generation:generation,scope:scope,mapper:mapper}
	if grant.Source==mapping.CategoryDerpibooru{
		w.taxonomy,err=taxonomy.NewManagedTaxonomyWindow(grant,subcategory,body,gate,generation,scope,transport)
		if err!=nil{return nil,ErrManagedMetadataEvidence}
	}
	return w,nil
}

func nativeMetadataMap(raw []byte)(map[string]any,error){
	d:=json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var meta map[string]any
	if d.Decode(&meta)!=nil||meta==nil{return nil,ErrManagedMetadataEvidence}
	return meta,nil
}

// Run refuses an incomplete window. It does not replace the legacy
// per-post outcome behavior; the candidate is intentionally disconnected.
// An outer generation-bound lease spans all validation, including the
// nested taxonomy lease. Cancellation before publication discards evidence.
func(w *ManagedEvidenceWindow)Run(caller context.Context,q workerproto.MetadataPlanRequest)([]ManagedEvidencePost,error){
	if w==nil||q!=w.grant||!w.used.CompareAndSwap(false,true){return nil,ErrManagedMetadataEvidence}
	ctx,finish,err:=w.gate.AdmitBound(caller,w.generation)
	if err!=nil{return nil,err}
	defer finish(nil)
	allowed:=func()bool{return ctx.Err()==nil&&caller.Err()==nil&&w.scope(ctx,q)}
	if !allowed(){return nil,ErrManagedMetadataEvidence}
	out:=make([]ManagedEvidencePost,0,len(w.posts))
	for _,p:=range w.posts{
		if !allowed(){return nil,ErrManagedMetadataEvidence}
		raw,err:=nativeMetadataMap(p.Native)
		if err!=nil{return nil,err}
		item:=gdl.Item{ID:p.ID,MediaURL:p.MediaURL,Category:kwdict.String(raw,"category"),
			Subcategory:kwdict.String(raw,"subcategory"),Num:kwdict.Num(raw),Meta:raw}
		if !item.HasStrictIdentity()||item.Category!=q.Source||mapping.ValidateStrictTags(raw)!=nil{
			return nil,ErrManagedMetadataEvidence
		}
		interpreted,err:=nativeMetadataMap(p.Native)
		if err!=nil{return nil,err}
		out=append(out,ManagedEvidencePost{Item:item,Interpreted:interpreted})
	}
	if w.taxonomy!=nil{
		rows,err:=w.taxonomy.Run(ctx,q)
		if err!=nil{return nil,err}
		if len(rows)!=len(out){return nil,ErrManagedMetadataEvidence}
		for i,row:=range rows{
			if row.ID!=out[i].Item.ID||!reflect.DeepEqual(row.Native,out[i].Item.Meta){
				return nil,ErrManagedMetadataEvidence
			}
			if _,ok:=row.Interpreted[kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag);!ok{
				return nil,ErrManagedMetadataEvidence
			}
			out[i].Interpreted=row.Interpreted
		}
	}
	for _,p:=range out{
		if !allowed()||w.mapper.ValidateSourceRating(p.Interpreted)!=nil{return nil,ErrManagedMetadataEvidence}
	}
	if !allowed(){return nil,ErrManagedMetadataEvidence}
	return out,nil
}
