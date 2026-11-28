package acquisitionstage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// Single mapped-post diagnostic, before duplicate disposal. The existing
// library service is the only edge writer. Caller must exclude scanner
// and other gallery edits. No replay after an attempt, even if no edge
// was added. This phase is not a terminal receipt.
const maxRelationshipRecord = 512 << 10

type relationshipEdge struct {
	Source int64 `json:"source"`
	Child int64 `json:"child"`
	Prior []int64 `json:"prior"`
}
type relationshipPlan struct {
	Parent int64 `json:"parent"`
	Children []int64 `json:"children"`
	Edges []relationshipEdge `json:"edges"`
}
type relationshipAttempt struct {
	Version int `json:"version"`
	MetadataSHA256 string `json:"metadata_sha256"`
	ImageID int64 `json:"image_id"`
	URL string `json:"url"`
	ParentURL string `json:"parent_url"`
	Before relationshipPlan `json:"before"`
}
type relationshipResult struct {
	Version int `json:"version"`
	AttemptSHA256 string `json:"attempt_sha256"`
	After relationshipPlan `json:"after"`
}

// Limit includes an overflow row. SQL errors/ambiguity are not absence.
func relationIDs(ctx context.Context, tx *sql.Tx, limit int, query string, arg any) ([]int64,error) {
	rows,err:=tx.QueryContext(ctx,query,arg,limit+1)
	if err!=nil{return nil,ErrStageRecovery}
	defer rows.Close()
	out:=make([]int64,0)
	for rows.Next(){
		var id int64
		if rows.Scan(&id)!=nil||id<=0||len(out)>=limit||(len(out)>0&&id<=out[len(out)-1]){return nil,ErrStageRecovery}
		out=append(out,id)
	}
	if rows.Err()!=nil{return nil,ErrStageRecovery}
	return out,nil
}

func inspectRelationships(ctx context.Context,g *library.Gallery,id int64,url,parentURL string)(relationshipPlan,error){
	var out relationshipPlan
	if g==nil||g.RelationsSvc==nil||id<=0||url==""{return out,ErrStageRefused}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return out,ErrStageRecovery}
	defer tx.Rollback()
	const byURL=`SELECT DISTINCT image_id FROM image_sources WHERE url=? ORDER BY image_id LIMIT ?`
	owners,err:=relationIDs(ctx,tx,1,byURL,url)
	if err!=nil||len(owners)!=1||owners[0]!=id{return out,ErrStageRecovery}
	out.Edges=make([]relationshipEdge,0)
	if parentURL!=""{
		parents,err:=relationIDs(ctx,tx,1,byURL,parentURL)
		if err!=nil{return out,err}
		if len(parents)==1{out.Parent=parents[0]}
	}
	out.Children,err=relationIDs(ctx,tx,256,`SELECT DISTINCT image_id FROM image_sources WHERE parent_url=? ORDER BY image_id LIMIT ?`,url)
	if err!=nil{return out,err}
	add:=func(source,child int64)error{
		if source==0||source==child{return nil}
		prior,err:=relationIDs(ctx,tx,64,`SELECT source_image_id FROM derivative_edges WHERE derivative_image_id=? ORDER BY source_image_id LIMIT ?`,child)
		if err!=nil{return err}
		out.Edges=append(out.Edges,relationshipEdge{Source:source,Child:child,Prior:prior})
		return nil
	}
	if err:=add(out.Parent,id);err!=nil{return out,err}
	for _,child:=range out.Children{if err:=add(id,child);err!=nil{return out,err}}
	if tx.Commit()!=nil||ctx.Err()!=nil{return out,ErrStageRecovery}
	return out,nil
}
func expectedRelationships(before relationshipPlan)relationshipPlan{
	after:=before
	after.Edges=append([]relationshipEdge{},before.Edges...)
	for i,e:=range after.Edges{
		if len(e.Prior)==0{after.Edges[i].Prior=[]int64{e.Source}}
	}
	return after
}
func sameRelationships(a,b relationshipPlan)bool{
	if a.Parent!=b.Parent||!slices.Equal(a.Children,b.Children)||len(a.Edges)!=len(b.Edges){return false}
	for i,e:=range a.Edges{
		f:=b.Edges[i]
		if e.Source!=f.Source||e.Child!=f.Child||!slices.Equal(e.Prior,f.Prior){return false}
	}
	return true
}

func(a *Allocator)relationshipContext(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)(relationshipAttempt,*os.Root,error){
	var zero relationshipAttempt
	if g==nil||g.WatcherRunning(){return zero,nil,ErrStageRefused}
	mapped,err:=a.observeMappedMetadataLocked(ctx,grant,g)
	if err!=nil||len(mapped)!=1{return zero,nil,ErrStageRecovery}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return zero,nil,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,nil,err}
	defer src.Close();defer dst.Close()
	fail:=func(err error)(relationshipAttempt,*os.Root,error){p.Close();return zero,nil,err}
	rows,err:=selectMappedRows(snapshot,e)
	if err!=nil||len(rows)!=1||rows[0].Provenance==nil||mapped[0].PostID!=rows[0].PostID{return fail(ErrStageRefused)}
	input,_,err:=normalizedMappedSource(rows[0])
	if err!=nil{return fail(err)}
	landing,err:=readLandingAttempt(p,r)
	if err!=nil{return fail(err)}
	current,err:=observedDB(r,g)
	if err!=nil||!sameLandingDB(current,landing){return fail(ErrStageRecovery)}
	raw,err:=boundedRegularBytes(p,"metadata-result.json",maxMetadataResultBytes)
	if err!=nil{return fail(err)}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return fail(ErrStageRefused)}
	current,err=observedDB(r,g)
	if err!=nil||!sameLandingDB(current,landing){return fail(ErrStageRecovery)}
	return relationshipAttempt{Version:1,MetadataSHA256:digestBytes(raw),ImageID:mapped[0].ImageID,URL:input.URL,ParentURL:input.ParentURL},p,nil
}
func sameRelationshipRoot(a,b *os.Root)bool{
	ai,err:=a.Stat(".")
	if err!=nil{return false}
	bi,err:=b.Stat(".")
	return err==nil&&os.SameFile(ai,bi)
}
func sameRelationshipScope(a,b relationshipAttempt)bool{
	return a.Version==b.Version&&a.MetadataSHA256==b.MetadataSHA256&&a.ImageID==b.ImageID&&a.URL==b.URL&&a.ParentURL==b.ParentURL
}
func(a *Allocator)applyRelationships(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	a.mu.Lock();defer a.mu.Unlock()
	m,p,err:=a.relationshipContext(ctx,grant,g)
	if err!=nil{return err}
	defer p.Close()
	for _,name:=range []string{"relationship-attempt.json","relationship-result.json","new-receipt.json","duplicate-attempt.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	m.Before,err=inspectRelationships(ctx,g,m.ImageID,m.URL,m.ParentURL)
	if err!=nil{return err}
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxRelationshipRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"relationship-attempt.json",raw);err!=nil{return err}
	syncDirectory(p)
	current,check,err:=a.relationshipContext(ctx,grant,g)
	if err!=nil{return err}
	sameRoot:=sameRelationshipRoot(p,check)
	check.Close()
	if !sameRoot||!sameRelationshipScope(m,current){return ErrStageRecovery}
	before,err:=inspectRelationships(ctx,g,m.ImageID,m.URL,m.ParentURL)
	if err!=nil||!sameRelationships(m.Before,before){return ErrStageRecovery}
	// Legacy service logs failures. Its return alone is not evidence.
	g.LinkParentRelations(m.ImageID,m.URL,m.ParentURL)
	after,err:=inspectRelationships(ctx,g,m.ImageID,m.URL,m.ParentURL)
	if err!=nil||!sameRelationships(expectedRelationships(m.Before),after){return ErrStageRecovery}
	current,check,err=a.relationshipContext(ctx,grant,g)
	if err!=nil{return err}
	sameRoot=sameRelationshipRoot(p,check)
	check.Close()
	if !sameRoot||!sameRelationshipScope(m,current){return ErrStageRecovery}
	result,err:=json.Marshal(relationshipResult{Version:1,AttemptSHA256:digestBytes(raw),After:after})
	if err!=nil||len(result)>maxRelationshipRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"relationship-result.json",result);err!=nil{return err}
	syncDirectory(p)
	return a.observeRelationshipsLocked(ctx,grant,g)
}
func(a *Allocator)observeRelationships(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeRelationshipsLocked(ctx,grant,g)
}
func(a *Allocator)observeRelationshipsLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	current,p,err:=a.relationshipContext(ctx,grant,g)
	if err!=nil{return err}
	defer p.Close()
	raw,err:=boundedRegularBytes(p,"relationship-attempt.json",maxRelationshipRecord)
	if err!=nil{return err}
	var m relationshipAttempt
	if json.Unmarshal(raw,&m)!=nil||!sameRelationshipScope(m,current){return ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return ErrStageRecovery}
	body,err:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil{return err}
	actual,err:=inspectRelationships(ctx,g,m.ImageID,m.URL,m.ParentURL)
	if err!=nil||!sameRelationships(expectedRelationships(m.Before),actual){return ErrStageRecovery}
	expected,err:=json.Marshal(relationshipResult{Version:1,AttemptSHA256:digestBytes(raw),After:actual})
	if err!=nil||!bytes.Equal(body,expected){return ErrStageRecovery}
	current,check,err:=a.relationshipContext(ctx,grant,g)
	if err!=nil{return err}
	sameRoot:=sameRelationshipRoot(p,check)
	check.Close()
	if !sameRoot||!sameRelationshipScope(m,current){return ErrStageRecovery}
	return nil
}

// Structural inventory validation is intentionally independent of DB
// reconciliation. A result cannot exist without its exact attempt.
// The attempt-only state remains inspectable but cannot be replayed.
func validateRelationshipRecords(p *os.Root,metadata metadataResult,hasResult bool)error{
	if len(metadata.Rows)!=1{return ErrStageRecovery}
	mraw,err:=json.Marshal(metadata)
	if err!=nil{return ErrStageRecovery}
	raw,err:=boundedRegularBytes(p,"relationship-attempt.json",maxRelationshipRecord)
	if err!=nil{return err}
	var m relationshipAttempt
	if json.Unmarshal(raw,&m)!=nil||m.Version!=1||m.MetadataSHA256!=digestBytes(mraw)||
		m.ImageID!=metadata.Rows[0].ImageID||m.URL==""||m.Before.Parent<0||
		(m.ParentURL==""&&m.Before.Parent!=0)||m.Before.Children==nil||m.Before.Edges==nil||len(m.Before.Children)>256||len(m.Before.Edges)>257{return ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return ErrStageRecovery}
	for i,id:=range m.Before.Children{
		if id<=0||(i>0&&id<=m.Before.Children[i-1]){return ErrStageRecovery}
	}
	// Verify the planned edge list follows exactly the legacy order.
	pairs:=make([][2]int64,0)
	if m.Before.Parent>0&&m.Before.Parent!=m.ImageID{pairs=append(pairs,[2]int64{m.Before.Parent,m.ImageID})}
	for _,id:=range m.Before.Children{if id!=m.ImageID{pairs=append(pairs,[2]int64{m.ImageID,id})}}
	if len(pairs)!=len(m.Before.Edges){return ErrStageRecovery}
	for i,e:=range m.Before.Edges{
		if e.Source!=pairs[i][0]||e.Child!=pairs[i][1]||e.Prior==nil||len(e.Prior)>64{return ErrStageRecovery}
		for j,id:=range e.Prior{
			if id<=0||id==e.Child||(j>0&&id<=e.Prior[j-1]){return ErrStageRecovery}
		}
	}
	if !hasResult{return nil}
	body,err:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil{return err}
	expected,err:=json.Marshal(relationshipResult{Version:1,AttemptSHA256:digestBytes(raw),After:expectedRelationships(m.Before)})
	if err!=nil||!bytes.Equal(body,expected){return ErrStageRecovery}
	return nil
}
