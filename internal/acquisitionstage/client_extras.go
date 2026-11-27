package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/tags"
)

// Isolated owner-authorized add-only client tags after relocation.
// No worker-supplied extras authority; no source/commentary override,
// custom via, collection or unrated rating mutation in this candidate.
type extrasAuthority func(context.Context,workerproto.StageAllocation)(contract.JobExtras,error)
const maxExtrasRecord = 512 << 10
const maxClientTags = 128
const maxClientClosure = 256
const maxClientImageTags = 8192

type clientTagState struct{
	Via string `json:"via"`
	TagIDs []int64 `json:"tag_ids"`
}
type clientResolution struct{
	Form string `json:"form"`
	Category int64 `json:"category"`
	Bare string `json:"bare"`
	ID int64 `json:"id"`
	Canonical bool `json:"canonical"`
	Rejected bool `json:"rejected"`
	Closure []int64 `json:"closure"`
}
type extrasAttempt struct{
	Version int `json:"version"`
	NamingSHA256 string `json:"naming_sha256"`
	ImageID int64 `json:"image_id"`
	Job contract.JobExtras `json:"job"`
	Before []int64 `json:"before"`
	Resolution []clientResolution `json:"resolution"`
}
type extrasResult struct{
	Version int `json:"version"`
	AttemptSHA256 string `json:"attempt_sha256"`
	Resolution []clientResolution `json:"resolution"`
	Client clientTagState `json:"client"`
	Rejected []string `json:"rejected"`
	AllTags []int64 `json:"all_tags"`
}
func validClientJob(j contract.JobExtras)bool{
	if j.Tags==nil||len(j.Tags)>maxClientTags||j.Source!=""||j.Commentary!=""||(j.Via!=""&&j.Via!=contract.Via){return false}
	for _,s:=range j.Tags{
		if len(s)>1024||!utf8.ValidString(s)||strings.ContainsRune(s,0){return false}
	}
	return true
}
func sortedClientIDs(ids []int64,limit int)bool{
	if ids==nil||len(ids)>limit{return false}
	for i,id:=range ids{if id<=0||(i>0&&ids[i-1]>=id){return false}}
	return true
}
func validClientResolutions(rs []clientResolution)bool{
	if rs==nil||len(rs)>maxClientTags{return false}
	for i,r:=range rs{
		if r.Form==""||len(r.Form)>1024||r.Bare==""||len(r.Bare)>1024||r.Category<=0||r.ID<0||
			(i>0&&rs[i-1].Form>=r.Form)||!sortedClientIDs(r.Closure,maxClientClosure){return false}
		if r.ID==0{if r.Canonical||r.Rejected||len(r.Closure)!=0{return false}
		}else if r.Rejected{if len(r.Closure)!=0{return false}
		}else if !slices.Contains(r.Closure,r.ID){return false}
	}
	return true
}
func expectedExtras(m extrasAttempt,raw []byte,after []clientResolution)(extrasResult,error){
	var zero extrasResult
	if m.Version!=1||m.ImageID<=0||!validDigest(m.NamingSHA256)||!validClientJob(m.Job)||
		!sortedClientIDs(m.Before,maxClientImageTags)||!validClientResolutions(m.Resolution)||!validClientResolutions(after)||len(after)!=len(m.Resolution){return zero,ErrStageRecovery}
	out:=extrasResult{Version:1,AttemptSHA256:digestBytes(raw),Resolution:after,Client:clientTagState{Via:contract.Via,TagIDs:[]int64{}},Rejected:[]string{},AllTags:append([]int64{},m.Before...)}
	for i,v:=range after{
		b:=m.Resolution[i]
		if b.Form!=v.Form||b.Category!=v.Category||b.Bare!=v.Bare||v.ID<=0{return zero,ErrStageRecovery}
		if b.ID>0{
			if !reflect.DeepEqual(b,v){return zero,ErrStageRecovery}
		}else if !v.Canonical||v.Rejected||len(v.Closure)!=1||v.Closure[0]!=v.ID{return zero,ErrStageRecovery}
		if v.Rejected{out.Rejected=append(out.Rejected,v.Form);continue}
		out.Client.TagIDs=append(out.Client.TagIDs,v.ID)
		out.AllTags=append(out.AllTags,v.Closure...)
	}
	slices.Sort(out.Client.TagIDs);out.Client.TagIDs=slices.Compact(out.Client.TagIDs)
	slices.Sort(out.AllTags);out.AllTags=slices.Compact(out.AllTags)
	if !sortedClientIDs(out.AllTags,maxClientImageTags){return zero,ErrStageLimit}
	return out,nil
}
func readExtrasAttempt(p *os.Root,metadata metadataResult)(extrasAttempt,[]byte,error){
	var zero extrasAttempt
	raw,err:=boundedRegularBytes(p,"extras-attempt.json",maxExtrasRecord)
	if err!=nil{return zero,nil,err}
	var m extrasAttempt
	naming,err:=boundedRegularBytes(p,"naming-result.json",maxNamingRecord)
	if err!=nil||len(metadata.Rows)!=1||json.Unmarshal(raw,&m)!=nil||m.Version!=1||m.ImageID!=metadata.Rows[0].ImageID||
		m.NamingSHA256!=digestBytes(naming)||!validClientJob(m.Job)||!sortedClientIDs(m.Before,maxClientImageTags)||!validClientResolutions(m.Resolution){return zero,nil,ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,nil,ErrStageRecovery}
	return m,raw,nil
}
func readExtrasResult(p *os.Root,m extrasAttempt,raw []byte)(extrasResult,error){
	var zero extrasResult
	body,err:=boundedRegularBytes(p,"extras-result.json",maxExtrasRecord)
	if err!=nil{return zero,err}
	var out extrasResult
	if json.Unmarshal(body,&out)!=nil{return zero,ErrStageRecovery}
	want,err:=expectedExtras(m,raw,out.Resolution)
	if err!=nil{return zero,err}
	canonical,err:=json.Marshal(want)
	if err!=nil||!bytes.Equal(body,canonical){return zero,ErrStageRecovery}
	return want,nil
}
func validateExtrasRecords(p *os.Root,l landingAttempt,metadata metadataResult,hasResult bool)error{
	if !l.NewOnly||l.RestoreImageID!=0{return ErrStageRecovery}
	m,raw,err:=readExtrasAttempt(p,metadata)
	if err!=nil{return err}
	if !hasResult{return nil}
	_,err=readExtrasResult(p,m,raw)
	return err
}
func activeClientImageTags(ctx context.Context,g *library.Gallery,id int64)([]int64,error){
	rows,err:=g.DB.Read.QueryContext(ctx,`SELECT tag_id,stale FROM image_tags WHERE image_id=? ORDER BY tag_id LIMIT ?`,id,maxClientImageTags+1)
	if err!=nil{return nil,ErrStageRecovery}
	defer rows.Close()
	out:=[]int64{}
	for rows.Next(){
		var tag int64;var stale int
		if rows.Scan(&tag,&stale)!=nil||stale!=0||len(out)>=maxClientImageTags{return nil,ErrStageRecovery}
		out=append(out,tag)
	}
	if rows.Err()!=nil||!sortedClientIDs(out,maxClientImageTags){return nil,ErrStageRecovery}
	return out,nil
}
// Resolve exactly as the shared legacy service, but never create tags.
// Refuse rating aliases/implications before mutation, even for an
// unrated source. A rejected name is a legitimate observed outcome.
func inspectClientResolution(ctx context.Context,g *library.Gallery,row acquisitionplan.ApprovedPolicyRow,j contract.JobExtras)([]clientResolution,error){
	if !validClientJob(j)||strings.EqualFold(row.SourceLabel,contract.Via){return nil,ErrStageRefused}
	var n int
	if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_sources WHERE site=? COLLATE NOCASE`,contract.Via).Scan(&n);err!=nil||n!=0{return nil,ErrStageRefused}
	categories,err:=g.TagSvc.ListCategories()
	if err!=nil{return nil,ErrStageRecovery}
	known:=map[string]bool{}
	var ratingID int64
	for _,c:=range categories{known[c.Name]=true;if c.Name=="rating"{ratingID=c.ID}}
	if ratingID<=0{return nil,ErrStageRecovery}
	forms:=contract.NormalizeClientTags(j.Tags,row.Rating!="",func(s string)bool{return known[s]})
	out:=[]clientResolution{}
	type key struct{cat int64;bare string}
	seen:=map[key]bool{}
	for _,form:=range forms{
		if ctx.Err()!=nil{return nil,ErrStageRefused}
		cat,bare,err:=gallery.ResolveCategoryTag(g.DB,form)
		if err!=nil||cat==ratingID{return nil,ErrStageRefused}
		bare,err=g.TagSvc.ValidateTagIn(bare,cat)
		if err!=nil||seen[key{cat,bare}]{return nil,ErrStageRefused}
		seen[key{cat,bare}]=true
		v:=clientResolution{Form:form,Category:cat,Bare:bare,Closure:[]int64{}}
		tag,err:=g.TagSvc.FindTag(bare,cat)
		if errors.Is(err,tags.ErrTagNotFound){out=append(out,v);continue}
		if err!=nil||tag==nil||tag.CategoryID==ratingID{return nil,ErrStageRefused}
		v.ID=tag.ID;v.Canonical=tag.Name==bare&&tag.CategoryID==cat;v.Rejected=tag.Rejected
		if !v.Rejected{
			ids,err:=g.TagSvc.ImplicationClosure(tag.ID)
			if err!=nil||len(ids)>maxClientClosure{return nil,ErrStageRecovery}
			slices.Sort(ids);v.Closure=slices.Compact(ids)
			for _,id:=range v.Closure{
				var c int64
				if err:=g.DB.Read.QueryRowContext(ctx,`SELECT category_id FROM tags WHERE id=?`,id).Scan(&c);err!=nil||c==ratingID{return nil,ErrStageRefused}
			}
		}
		out=append(out,v)
	}
	if !validClientResolutions(out){return nil,ErrStageRecovery}
	return out,nil
}
func(a *Allocator)extrasContext(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,auth extrasAuthority)(extrasAttempt,*os.Root,error){
	var zero extrasAttempt
	if g==nil||g.WatcherRunning()||auth==nil{return zero,nil,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,nil,err}
	defer src.Close();defer dst.Close()
	fail:=func(err error)(extrasAttempt,*os.Root,error){p.Close();return zero,nil,err}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return fail(err)}
	rows,err:=newOnlyRows(e,snapshot)
	if err!=nil||len(rows)!=1{return fail(ErrStageRefused)}
	l,err:=readLandingAttempt(p,r)
	if err!=nil||!l.NewOnly{return fail(ErrStageRefused)}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return fail(err)}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return fail(err)}
	if err:=validateNamingRecords(p,r,e,l,result,true);err!=nil{return fail(err)}
	naming,err:=boundedRegularBytes(p,"naming-result.json",maxNamingRecord)
	if err!=nil{return fail(err)}
	job,err:=auth(ctx,grant)
	job.Tags=append([]string{},job.Tags...)
	if err!=nil||!validClientJob(job){return fail(ErrStageRefused)}
	resolved,err:=inspectClientResolution(ctx,g,rows[0],job)
	if err!=nil{return fail(err)}
	s,err:=a.scope(ctx,grant.Request)
	current,dbErr:=observedDB(r,g)
	if err!=nil||!matches(r,s)||dbErr!=nil||!sameLandingDB(current,l)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return fail(ErrStageRefused)}
	return extrasAttempt{Version:1,NamingSHA256:digestBytes(naming),ImageID:result.Rows[0].ImageID,Job:job,Resolution:resolved},p,nil
}
func sameExtrasScope(a,b extrasAttempt)bool{
	return a.Version==b.Version&&a.NamingSHA256==b.NamingSHA256&&a.ImageID==b.ImageID&&reflect.DeepEqual(a.Job,b.Job)
}
func(a *Allocator)applyClientExtras(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,auth extrasAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	if err:=a.observeNamingLocked(ctx,grant,g,naming);err!=nil{return err}
	m,p,err:=a.extrasContext(ctx,grant,g,auth)
	if err!=nil{return err}
	defer p.Close()
	for _,name:=range []string{"extras-attempt.json","extras-result.json","composed-receipt.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	m.Before,err=activeClientImageTags(ctx,g,m.ImageID)
	if err!=nil{return err}
	// Bound possible result before the first write. Unknown tags each
	// contribute at most one new ID, known implications are bounded.
	budget:=len(m.Before)
	for _,v:=range m.Resolution{budget+=max(1,len(v.Closure))}
	if budget>maxClientImageTags{return ErrStageLimit}
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxExtrasRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"extras-attempt.json",raw);err!=nil{return err}
	syncDirectory(p)
	again,check,err:=a.extrasContext(ctx,grant,g,auth)
	if err!=nil{return err}
	sameRoot:=sameRelationshipRoot(p,check);check.Close()
	if !sameRoot||!sameExtrasScope(m,again)||!reflect.DeepEqual(m.Resolution,again.Resolution){return ErrStageRecovery}
	if err:=a.observeNamingLocked(ctx,grant,g,naming);err!=nil{return err}
	before,err:=activeClientImageTags(ctx,g,m.ImageID)
	if err!=nil||!slices.Equal(m.Before,before){return ErrStageRecovery}
	forms:=make([]string,0,len(m.Resolution))
	for _,v:=range m.Resolution{forms=append(forms,v.Form)}
	_,warnings,rejected,err:=g.ApplyInitialTags(m.ImageID,forms,contract.Via)
	if err!=nil||len(warnings)!=0{return ErrStageRecovery}
	after,check,err:=a.extrasContext(ctx,grant,g,auth)
	if err!=nil{return err}
	sameRoot=sameRelationshipRoot(p,check);check.Close()
	if !sameRoot||!sameExtrasScope(m,after){return ErrStageRecovery}
	result,err:=expectedExtras(m,raw,after.Resolution)
	if err!=nil||!slices.Equal(rejected,result.Rejected){return ErrStageRecovery}
	if err:=a.observeNamingWithClientLocked(ctx,grant,g,naming,&result.Client);err!=nil{return err}
	all,err:=activeClientImageTags(ctx,g,m.ImageID)
	if err!=nil||!slices.Equal(all,result.AllTags){return ErrStageRecovery}
	body,err:=json.Marshal(result)
	if err!=nil||len(body)>maxExtrasRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"extras-result.json",body);err!=nil{return err}
	syncDirectory(p)
	_,err=a.observeClientExtrasLocked(ctx,grant,g,naming,auth)
	return err
}
func(a *Allocator)observeClientExtrasLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,auth extrasAuthority)(extrasResult,error){
	var zero extrasResult
	current,p,err:=a.extrasContext(ctx,grant,g,auth)
	if err!=nil{return zero,err}
	defer p.Close()
	// The exact metadata/naming binding was structurally verified by
	// publicationRoots; re-read the same canonical attempt and outcome.
	raw,err:=boundedRegularBytes(p,"extras-attempt.json",maxExtrasRecord)
	var m extrasAttempt
	if err!=nil||json.Unmarshal(raw,&m)!=nil||!sameExtrasScope(m,current){return zero,ErrStageRecovery}
	result,err:=readExtrasResult(p,m,raw)
	if err!=nil||!reflect.DeepEqual(result.Resolution,current.Resolution){return zero,ErrStageRecovery}
	if err:=a.observeNamingWithClientLocked(ctx,grant,g,naming,&result.Client);err!=nil{return zero,err}
	all,err:=activeClientImageTags(ctx,g,m.ImageID)
	if err!=nil||!slices.Equal(all,result.AllTags){return zero,ErrStageRecovery}
	again,check,err:=a.extrasContext(ctx,grant,g,auth)
	if err!=nil{return zero,err}
	sameRoot:=sameRelationshipRoot(p,check);check.Close()
	if !sameRoot||!sameExtrasScope(m,again)||!reflect.DeepEqual(result.Resolution,again.Resolution){return zero,ErrStageRecovery}
	return result,nil
}
func(a *Allocator)observeClientExtras(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,auth extrasAuthority)(extrasResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeClientExtrasLocked(ctx,grant,g,naming,auth)
}
