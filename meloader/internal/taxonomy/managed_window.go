package taxonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/kwdict"
)

var ErrManagedTaxonomy = errors.New("managed source taxonomy refused")

const maxManagedTaxonomyResponse = 4 << 20
const maxManagedTaxonomyRequests = 4096

// ManagedTaxonomyScope is a trusted local authority callback, not a wire
// credential. The owner must bind live gallery/job/metadata generation.
// It must be checked before every request and before returning results.
type ManagedTaxonomyScope func(context.Context,workerproto.MetadataPlanRequest)bool

// ManagedTaxonomyWindow consumes an independently trusted metadata
// window, never a worker-supplied HTTP URL, search expression or cache.
// A private resolver uses only source-derived tag lookups and the existing
// reciprocal alias checks. No production caller/dispatcher is attached.
type ManagedTaxonomyWindow struct{
	grant workerproto.MetadataPlanRequest
	native []workerproto.MetadataPost
	gate *workerproto.DrainGate
	scope ManagedTaxonomyScope
	transport http.RoundTripper
	used atomic.Bool
}

// transport is a trusted local transport, never a requester option. It
// must honor request cancellation and complete/close response bodies.
// This candidate does not prove socket-level cleanup or DNS confinement.
func NewManagedTaxonomyWindow(grant workerproto.MetadataPlanRequest, subcategory string, body []byte,
	gate *workerproto.DrainGate, scope ManagedTaxonomyScope, transport http.RoundTripper)(*ManagedTaxonomyWindow,error){
	if grant.Source!="derpibooru"||gate==nil||scope==nil||transport==nil{return nil,ErrManagedTaxonomy}
	native,err:=workerproto.DecodeMetadataEvidence(body,grant,subcategory)
	if err!=nil{return nil,ErrManagedTaxonomy}
	return &ManagedTaxonomyWindow{grant:grant,native:native,gate:gate,scope:scope,transport:transport},nil
}

// Only the exact validated tag names/IDs can be searched. Alias-target
// requests are minted by the private resolver from validated records,
// never passed in by the worker. The existing resolver checks the slug
// and reciprocal incoming alias before interpretation.
type managedTagTransport struct{
	base http.RoundTripper
	allowedNames,allowedIDs map[string]bool
	allowed func(context.Context)bool
	count atomic.Int32
}

func (t *managedTagTransport) permitted(req *http.Request)bool{
	u:=req.URL
	if req.Method!=http.MethodGet||req.Body!=nil||u==nil||u.Scheme!="https"||
		u.Host!="derpibooru.org"||u.User!=nil||u.Fragment!=""||u.Opaque!=""||req.Host!=""&&req.Host!=u.Host||
		!t.allowed(req.Context()){return false}
	if u.Path=="/api/v1/json/search/tags"{
		q,err:=url.ParseQuery(u.RawQuery)
		if err!=nil||len(q)!=3||len(q["q"])!=1||len(q["page"])!=1||len(q["per_page"])!=1||
			q.Get("page")!="1"{return false}
		n,err:=strconv.Atoi(q.Get("per_page"))
		if err!=nil||n<1||n>50{return false}
		query:=q.Get("q")
		if t.allowedNames[query]{
			// Never permit a literal name to introduce another search
			// term or query operator. ID-based lookup remains available.
			return !strings.ContainsAny(query,"|&!()*~")&&!strings.Contains(query,"..")&&!strings.HasPrefix(strings.ToLower(query),"id:")
		}
		terms:=strings.Split(query," || ")
		if len(terms)>50||len(terms)!=n{return false}
		for _,term:=range terms{
			id,ok:=strings.CutPrefix(term,"id:")
			if !ok||!t.allowedIDs[id]{return false}
		}
		return true
	}
	const prefix="/api/v1/json/tags/"
	slug,ok:=strings.CutPrefix(u.Path,prefix)
	if !ok||u.RawQuery!=""||len(slug)>workerproto.MaxResolveNameBytes||slug=="."||slug==".."{return false}
	_,ok=philomenaSlugName(slug)
	return ok
}

func(t *managedTagTransport)RoundTrip(req *http.Request)(*http.Response,error){
	if !t.permitted(req)||t.count.Add(1)>maxManagedTaxonomyRequests{return nil,ErrManagedTaxonomy}
	resp,err:=t.base.RoundTrip(req)
	if err!=nil||resp==nil||resp.Body==nil{
		if resp!=nil&&resp.Body!=nil{_ = resp.Body.Close()}
		return nil,ErrManagedTaxonomy
	}
	// Consume and close the real response under the same admission lease;
	// bounded even on error/status/redirect. No raw remote error is exposed.
	data,readErr:=io.ReadAll(io.LimitReader(resp.Body,maxManagedTaxonomyResponse+1))
	closeErr:=resp.Body.Close()
	if readErr!=nil||closeErr!=nil||len(data)>maxManagedTaxonomyResponse||!t.allowed(req.Context())||
		resp.StatusCode>=300&&resp.StatusCode<400{return nil,ErrManagedTaxonomy}
	resp.Body=io.NopCloser(bytes.NewReader(data))
	resp.ContentLength=int64(len(data))
	return resp,nil
}

type ManagedTaxonomyPost struct{
	ID string
	// Native and Interpreted are independent maps. Native is never
	// overwritten with the alias/category enrichment used for filtering.
	Native map[string]any
	Interpreted map[string]any
}

func decodeManagedNative(raw []byte)(map[string]any,error){
	d:=json.NewDecoder(bytes.NewReader(raw));d.UseNumber()
	var m map[string]any
	if d.Decode(&m)!=nil||m==nil{return nil,ErrManagedTaxonomy}
	return m,nil
}

// Run is all-or-nothing for this candidate window. This intentionally
// does not replace legacy partial per-post outcomes. It does not grant
// transfer permission; the caller must still validate source ratings
// and apply the user's blacklist to the interpreted evidence.
func(w *ManagedTaxonomyWindow)Run(caller context.Context,request workerproto.MetadataPlanRequest)([]ManagedTaxonomyPost,error){
	if w==nil||request!=w.grant||!w.used.CompareAndSwap(false,true){return nil,ErrManagedTaxonomy}
	ctx,finish,err:=w.gate.Admit(caller)
	if err!=nil{return nil,err}
	defer finish(nil)
	ctx,cancel:=context.WithTimeout(ctx,90*time.Second)
	defer cancel()
	allowed:=func(c context.Context)bool{return c.Err()==nil&&ctx.Err()==nil&&caller.Err()==nil&&w.scope(c,w.grant)}
	if !allowed(ctx){return nil,ErrManagedTaxonomy}
	t:=&managedTagTransport{base:w.transport,allowedNames:map[string]bool{},allowedIDs:map[string]bool{},allowed:allowed}
	out:=make([]ManagedTaxonomyPost,0,len(w.native))
	posts:=make([]map[string]any,0,len(w.native))
	for _,post:=range w.native{
		if !allowed(ctx){return nil,ErrManagedTaxonomy}
		native,err:=decodeManagedNative(post.Native)
		if err!=nil{return nil,err}
		interpreted,err:=decodeManagedNative(post.Native)
		if err!=nil{return nil,err}
		names,lookups,err:=derpibooruPostLookups(native)
		if err!=nil{return nil,ErrManagedTaxonomy}
		for i,name:=range names{
			t.allowedNames[name]=true
			if lookups[i].id!=""{t.allowedIDs[lookups[i].id]=true}
		}
		out=append(out,ManagedTaxonomyPost{ID:post.ID,Native:native,Interpreted:interpreted})
		posts=append(posts,interpreted)
	}
	client:=&http.Client{Transport:t,Timeout:15*time.Second,CheckRedirect:func(*http.Request,[]*http.Request)error{return ErrManagedTaxonomy}}
	resolver,err:=newDerpibooru("",derpibooruBaseURL,client)
	if err!=nil{return nil,ErrManagedTaxonomy}
	errs:=resolver.EnrichBatch(ctx,request.Source,posts)
	if len(errs)!=len(posts)||!allowed(ctx){return nil,ErrManagedTaxonomy}
	for i,e:=range errs{
		if _,ok:=posts[i][kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag);e!=nil||!ok{return nil,ErrManagedTaxonomy}
	}
	if !allowed(ctx){return nil,ErrManagedTaxonomy}
	return out,nil
}
