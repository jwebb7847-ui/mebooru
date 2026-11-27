package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// First composed terminal candidate: single new PNG, approved source
// evidence + relationship witness + actual relocated canonical + explicit
// owner job extras (including empty). No collection, duplicate or restore
// and no permission to reclaim stage. No production caller yet.
const composedReceiptKind = "isolated_named_new_png_client_v1"
const maxComposedReceipt = 128 << 10
type composedReceipt struct{
	Version int `json:"version"`
	Kind string `json:"kind"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	GalleryIdentity string `json:"gallery_identity"`
	PolicySHA256 string `json:"policy_sha256"`
	NamingSHA256 string `json:"naming_sha256"`
	ExtrasSHA256 string `json:"extras_sha256"`
	Row newReceiptRow `json:"row"`
	PathID int64 `json:"path_id"`
	FileIdentity string `json:"file_identity"`
}
func expectedComposedReceipt(p *os.Root,r record,e SealedEvidence,l landingAttempt,m metadataAttempt,metadata metadataResult)(composedReceipt,error){
	var zero composedReceipt
	if !l.NewOnly||l.RestoreImageID!=0||len(e.Files)!=1||len(metadata.Rows)!=1||len(m.Targets)!=1||m.Targets[0].PriorRating!=""{return zero,ErrStageRecovery}
	if err:=validateNamingRecords(p,r,e,l,metadata,true);err!=nil{return zero,err}
	n,_,err:=readNamingAttempt(p,r,e,metadata)
	if err!=nil{return zero,err}
	if err:=validateExtrasRecords(p,l,metadata,true);err!=nil{return zero,err}
	naming,err:=boundedRegularBytes(p,"naming-result.json",maxNamingRecord)
	if err!=nil{return zero,err}
	extras,err:=boundedRegularBytes(p,"extras-result.json",maxExtrasRecord)
	if err!=nil{return zero,err}
	f:=e.Files[0];v:=metadata.Rows[0]
	return composedReceipt{Version:1,Kind:composedReceiptKind,Allocation:r.Allocation,GalleryIdentity:r.GalleryIdentity,
		PolicySHA256:m.PolicySHA256,NamingSHA256:digestBytes(naming),ExtrasSHA256:digestBytes(extras),
		Row:newReceiptRow{PostID:f.ID,ImageID:v.ImageID,Name:n.Final,Size:f.Size,SHA256:f.SHA256},PathID:n.PathID,FileIdentity:n.FileIdentity},nil
}
func readComposedReceipt(p *os.Root,r record,e SealedEvidence,l landingAttempt,m metadataAttempt,metadata metadataResult)(composedReceipt,error){
	var zero composedReceipt
	want,err:=expectedComposedReceipt(p,r,e,l,m,metadata)
	if err!=nil{return zero,err}
	raw,err:=boundedRegularBytes(p,"composed-receipt.json",maxComposedReceipt)
	if err!=nil{return zero,err}
	canonical,err:=json.Marshal(want)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	return want,nil
}
func(a *Allocator)composedReceiptLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,write bool)(composedReceipt,error){
	var zero composedReceipt
	actual,err:=a.observeClientExtrasLocked(ctx,grant,g,naming,extras)
	if err!=nil{return zero,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	l,err:=readLandingAttempt(p,r)
	if err!=nil{return zero,err}
	m,err:=readMetadataAttempt(p,l,e)
	if err!=nil{return zero,err}
	result,err:=readMetadataResult(p,m)
	if err!=nil{return zero,err}
	want,err:=expectedComposedReceipt(p,r,e,l,m,result)
	if err!=nil{return zero,err}
	raw,err:=json.Marshal(actual)
	if err!=nil||digestBytes(raw)!=want.ExtrasSHA256{return zero,ErrStageRecovery}
	s,err:=a.scope(ctx,grant.Request)
	current,dbErr:=observedDB(r,g)
	if err!=nil||!matches(r,s)||dbErr!=nil||!sameLandingDB(current,l)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
	if !write{return readComposedReceipt(p,r,e,l,m,result)}
	if _,err:=p.Lstat("composed-receipt.json");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	body,err:=json.Marshal(want)
	if err!=nil||len(body)>maxComposedReceipt{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"composed-receipt.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	// Writer must pass the exact same independent observation as a
	// reopened allocator. No retry, cleanup or acknowledgement on error.
	return a.composedReceiptLocked(ctx,grant,g,naming,extras,false)
}
func(a *Allocator)finishComposedReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(composedReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.composedReceiptLocked(ctx,grant,g,naming,extras,true)
}
func(a *Allocator)observeComposedReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(composedReceipt,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.composedReceiptLocked(ctx,grant,g,naming,extras,false)
}
