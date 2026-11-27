package acquisitionstage

import(
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"os"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// A durable record of the completed *mapped metadata phase*, not a
// terminal import receipt. Its absence after attempt remains ambiguous.
const maxMetadataResultBytes = 2 << 20

type metadataResult struct{
	Version int `json:"version"`
	AttemptSHA256 string `json:"attempt_sha256"`
	Rows []metadataObservation `json:"rows"`
}
func metadataAttemptDigest(m metadataAttempt)(string,error){
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxSealRecordBytes{return "",ErrStageRecovery}
	return digestBytes(raw),nil
}
func sameMetadataObservation(a,b metadataObservation)bool{
	return a.PostID==b.PostID&&a.ImageID==b.ImageID&&a.Rating==b.Rating&&
		slices.Equal(a.Rejected,b.Rejected)&&slices.Equal(a.TagIDs,b.TagIDs)
}
func readMetadataResult(p *os.Root,m metadataAttempt)(metadataResult,error){
	var zero metadataResult
	raw,err:=boundedRegularBytes(p,"metadata-result.json",maxMetadataResultBytes)
	if err!=nil{return zero,err}
	var result metadataResult
	hash,err:=metadataAttemptDigest(m)
	if err!=nil||json.Unmarshal(raw,&result)!=nil||result.Version!=1||result.AttemptSHA256!=hash||
		result.Rows==nil||len(result.Rows)!=len(m.Targets){return zero,ErrStageRecovery}
	for i,v:=range result.Rows{
		t:=m.Targets[i]
		if v.PostID!=t.PostID||v.ImageID!=t.ImageID||!canonicalRating(v.Rating)||
			(t.PriorRating!=""&&v.Rating!=t.PriorRating)||v.Rejected==nil||v.TagIDs==nil{return zero,ErrStageRecovery}
		var previous int64
		for _,id:=range v.TagIDs{
			if id<=previous{return zero,ErrStageRecovery}
			previous=id
		}
		seen:=map[string]bool{}
		for _,name:=range v.Rejected{
			if name==""||seen[name]{return zero,ErrStageRecovery}
			seen[name]=true
		}
	}
	canonical,err:=json.Marshal(result)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return result,nil
}

func(a *Allocator)observeMappedMetadata(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)([]metadataObservation,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeMappedMetadataLocked(ctx,grant,g)
}
func(a *Allocator)observeMappedMetadataLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)([]metadataObservation,error){
	if g==nil||g.WatcherRunning(){return nil,ErrStageRefused}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return nil,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return nil,err}
	defer p.Close();defer src.Close();defer dst.Close()
	landing,err:=readLandingAttempt(p,r)
	if err!=nil{return nil,err}
	current,err:=observedDB(r,g)
	current.NewOnly=landing.NewOnly
	if err!=nil||current!=landing{return nil,ErrStageRecovery}
	m,err:=readMetadataAttempt(p,landing,e)
	if err!=nil{return nil,err}
	_,digest,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	if err!=nil||digest!=m.PolicySHA256{return nil,ErrStageRecovery}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return nil,err}
	rows,err:=selectMappedRows(snapshot,e)
	if err!=nil{return nil,err}
	landed,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||len(landed)!=len(rows){return nil,ErrStageRecovery}
	for i,row:=range rows{
		t:=m.Targets[i]
		if landed[i].PostID!=t.PostID||landed[i].ImageID!=t.ImageID||landed[i].State!="observed_path"{return nil,ErrStageRecovery}
		got,err:=verifyMappedRow(ctx,g,t,row,result.Rows[i].Rejected)
		if err!=nil||!sameMetadataObservation(got,result.Rows[i]){return nil,ErrStageRecovery}
	}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s)||g.WatcherRunning(){return nil,ErrStageRefused}
	current,err=observedDB(r,g)
	current.NewOnly=landing.NewOnly
	if err!=nil||current!=landing{return nil,ErrStageRecovery}
	return result.Rows,nil
}
