package acquisitionstage

import (
	"context"
	"encoding/json"
	"image/png"
	"path/filepath"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// First guarded publication candidate: one genuinely new PNG, explicit
// source provenance, no relationship/collection claim, supported client
// tags, existing destination directories and known pre-landing naming
// facts. Neither this snapshot nor a successful link is a terminal receipt
// or production authorization. Owner must still exclude other writers.
type newPublicationPlan struct {
	DB landingAttempt
	PolicySHA256 string
	File acquisitionplan.VerifiedTransferFile
	FileIdentity string
	DestinationIdentity [2]string
	Naming gallery.ReceivedNamingRequest
	Preview gallery.ReceivedNamingPreview
	Job contract.JobExtras
	Client []clientResolution
}

func(a *Allocator)newPublicationPlanLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(newPublicationPlan,error){
	var zero newPublicationPlan
	if g==nil||g.WatcherRunning()||naming==nil||extras==nil{return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRefused}
	db,err:=observedDB(r,g)
	if err!=nil{return zero,err}
	places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
	if err!=nil||len(places)!=1||(places[0].State!="prepared"&&places[0].State!="absent"){return zero,ErrStageRecovery}
	f:=e.Files[0]
	// The deterministic diagnostic filename must have neither a file nor
	// a pre-existing row, even if a stale row has no on-disk file.
	var n int
	if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_paths WHERE path=?`,filepath.Join(r.GalleryRoot,finalName(grant,f))).Scan(&n);err!=nil||n!=0{return zero,ErrStageRefused}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return zero,err}
	rows,err:=newOnlyRows(e,snapshot)
	if err!=nil||len(rows)!=1{return zero,ErrStageRefused}
	if err:=verifyNewOnlyEligibility(ctx,g,e,snapshot);err!=nil{return zero,err}
	_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	if err!=nil{return zero,ErrStageRecovery}
	row:=rows[0]
	identity,err:=namingFile(ctx,src,f.RelativeName,f,1)
	if err!=nil{return zero,err}
	file,err:=src.Open(f.RelativeName)
	if err!=nil{return zero,ErrStageRecovery}
	cfg,decodeErr:=png.DecodeConfig(file)
	closeErr:=file.Close()
	if decodeErr!=nil||closeErr!=nil||cfg.Width<=0||cfg.Height<=0{return zero,ErrStageRefused}
	req,err:=naming(ctx,grant)
	if err!=nil||!validNamingRequest(req)||req.Gallery!=g.Name||req.ReceivedName!=f.RelativeName||req.Source!=row.SourceLabel||req.PostID!=row.PostID{return zero,ErrStageRefused}
	preview,err:=gallery.PreviewUnlandedReceivedNaming(ctx,g.DB,g.Boundary(),req,f.SHA256,f.Size)
	if err!=nil{return zero,ErrStageRefused}
	var destinations [2]string
	for i,path:=range []string{preview.InitialPath,preview.FinalPath}{
		rel,err:=namingRelative(r.GalleryRoot,path)
		if err!=nil||strings.EqualFold(rel,finalName(grant,f)){return zero,ErrStageRefused}
		parent,_,err:=namingParent(dst,rel)
		if err!=nil{return zero,err}
		live:=namingParentLive(r,dst,parent,rel)
		destinations[i],err=rootIdentity(parent)
		parent.Close()
		if err!=nil||!live{return zero,ErrStageRefused}
	}
	job,err:=extras(ctx,grant)
	if err!=nil{return zero,ErrStageRefused}
	// Match the existing extras context: empty tags are represented
	// explicitly, never by omitting the owner job decision.
	job.Tags=append([]string{},job.Tags...)
	client,err:=inspectClientResolution(ctx,g,row,job)
	if err!=nil{return zero,err}
	current,err:=observedDB(r,g)
	s,scopeErr:=a.scope(ctx,grant.Request)
	if err!=nil||!sameLandingDB(db,current)||scopeErr!=nil||!matches(r,s)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
	currentID,err:=namingFile(ctx,src,f.RelativeName,f,1)
	if err!=nil||identity!=currentID{return zero,ErrStageRecovery}
	return newPublicationPlan{DB:db,PolicySHA256:hash,File:f,FileIdentity:identity,DestinationIdentity:destinations,Naming:req,Preview:preview,Job:job,Client:client},nil
}

// Bind the caller's read-only preview to the exact current plan at both
// publication boundaries, including after the synced attempt marker.
// No callback can authorize a different plan. Refusal after the marker
// retains uncertain state and never retries the link. Existing isolated
// diagnostic entry points are not mounted in production and are unchanged.
func(a *Allocator)planNewPublication(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(newPublicationPlan,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.newPublicationPlanLocked(ctx,grant,g,naming,extras)
}
func(a *Allocator)publishPlannedNew(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,expected newPublicationPlan)([]placement,error){
	a.mu.Lock();defer a.mu.Unlock()
	raw,err:=json.Marshal(expected)
	if err!=nil{return nil,ErrStageRefused}
	guard:=func()error{
		current,err:=a.newPublicationPlanLocked(ctx,grant,g,naming,extras)
		if err!=nil{return err}
		again,err:=json.Marshal(current)
		if err!=nil||string(raw)!=string(again){return ErrStageRefused}
		return nil
	}
	return a.publishPreparedLocked(ctx,grant,guard)
}
