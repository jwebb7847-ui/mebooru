package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// Guarded publication candidate: one genuinely new native image, explicit
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
	Media *plannedImageFacts `json:",omitempty"`
}


// A guarded publication uses a distinct canonical versioned attempt,
// not the old raw transfer-ID marker. The exact plan remains available
// across restart and reclamation; no worker can supply this record.
const maxPlannedAttemptBytes = 1 << 20
type plannedPublicationAttempt struct {
	Version int
	Plan newPublicationPlan
}

func encodePlannedAttempt(plan newPublicationPlan)([]byte,error){
	version:=2
	if plan.Media!=nil{version=3}
	body,err:=json.Marshal(plannedPublicationAttempt{Version:version,Plan:plan})
	if err!=nil||len(body)>maxPlannedAttemptBytes{return nil,ErrStageLimit}
	return body,nil
}

// nil plan is the historical diagnostic marker only. All other content
// must be a complete canonical guarded marker bound to this allocation.
func readPublicationAttempt(p *os.Root,grant workerproto.StageAllocation)(*newPublicationPlan,string,error){
	body,err:=boundedRegularBytes(p,"attempt",maxPlannedAttemptBytes)
	if err!=nil{return nil,"",err}
	if string(body)==grant.TransferID{return nil,"",nil}
	var marker plannedPublicationAttempt
	if json.Unmarshal(body,&marker)!=nil||(marker.Version!=2&&marker.Version!=3){return nil,"",ErrStageRecovery}
	v:=marker.Plan
	canonical,err:=encodePlannedAttempt(v)
	if err!=nil||!bytes.Equal(body,canonical){return nil,"",ErrStageRecovery}
	// The pre-landing DB witness has no disposition or recovery fields.
	db:=landingAttempt{Version:1,Allocation:grant,GalleryIdentity:v.DB.GalleryIdentity,DBPath:v.DB.DBPath,DBFileIdentity:v.DB.DBFileIdentity}
	if v.DB!=db||db.GalleryIdentity==""||len(db.GalleryIdentity)>1024||!filepath.IsAbs(db.DBPath)||filepath.Clean(db.DBPath)!=db.DBPath||
		db.DBFileIdentity==""||len(db.DBFileIdentity)>128||!validDigest(v.PolicySHA256)||!validResultFile(v.File)||
		(marker.Version==2&&(v.Media!=nil||filepath.Ext(v.File.RelativeName)!=".png"))||(marker.Version==3&&!validPlannedImageFacts(v.File,v.Media))||
		v.FileIdentity==""||len(v.FileIdentity)>128||!validNamingRequest(v.Naming)||v.Naming.Gallery!=grant.Request.Transfer.Metadata.Gallery||
		v.Naming.ReceivedName!=v.File.RelativeName||v.Naming.PostID!=v.File.ID||!validClientJob(v.Job)||!validClientResolutions(v.Client){return nil,"",ErrStageRecovery}
	for i,path:=range []string{v.Preview.InitialPath,v.Preview.FinalPath}{
		if !filepath.IsAbs(path)||filepath.Clean(path)!=path||v.DestinationIdentity[i]==""||len(v.DestinationIdentity[i])>128{return nil,"",ErrStageRecovery}
	}
	return &v,digestBytes(body),nil
}

func verifyPlanDestinations(r record,dst *os.Root,plan *newPublicationPlan)error{
	if plan==nil{return nil}
	for i,path:=range []string{plan.Preview.InitialPath,plan.Preview.FinalPath}{
		rel,err:=namingRelative(r.GalleryRoot,path)
		if err!=nil||strings.EqualFold(rel,finalName(r.Allocation,plan.File)){return ErrStageRefused}
		parent,_,err:=namingParent(dst,rel)
		if err!=nil{return err}
		id,err:=rootIdentity(parent)
		live:=namingParentLive(r,dst,parent,rel)
		parent.Close()
		if err!=nil||!live||id!=plan.DestinationIdentity[i]{return ErrStageRecovery}
	}
	return nil
}

func samePublicationPlan(a,b newPublicationPlan)bool{
	// Historical v2 PNG plans did not record measured media facts.
	if a.Media==nil&&filepath.Ext(a.File.RelativeName)==".png"{b.Media=nil}
	left,err:=encodePlannedAttempt(a)
	right,otherErr:=encodePlannedAttempt(b)
	return err==nil&&otherErr==nil&&bytes.Equal(left,right)
}

func(a *Allocator)newPublicationPlanLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,placed bool)(newPublicationPlan,error){
	var zero newPublicationPlan
	if g==nil||g.WatcherRunning()||naming==nil||extras==nil{return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRefused}
	db,err:=observedDB(r,g)
	if err!=nil{return zero,err}
	places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
	if err!=nil||len(places)!=1{return zero,ErrStageRecovery}
	links:=uint64(1)
	if placed{if places[0].State!="placed"{return zero,ErrStageRecovery};links=2}else if places[0].State!="prepared"&&places[0].State!="absent"{return zero,ErrStageRecovery}
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
	identity,err:=namingFile(ctx,src,f.RelativeName,f,links)
	if err!=nil{return zero,err}
	media,err:=inspectPlannedImage(ctx,src,f,links)
	if err!=nil{return zero,err}
	req,err:=naming(ctx,grant)
	if err!=nil||!validNamingRequest(req)||req.Gallery!=g.Name||req.ReceivedName!=f.RelativeName||req.Source!=row.SourceLabel||req.PostID!=row.PostID{return zero,ErrStageRefused}
	preview,err:=gallery.PreviewMeasuredReceivedNaming(ctx,g.DB,g.Boundary(),req,f.SHA256,f.Size,media.Format,media.Width,media.Height,media.MD5)
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
	currentID,err:=namingFile(ctx,src,f.RelativeName,f,links)
	if err!=nil||identity!=currentID{return zero,ErrStageRecovery}
	return newPublicationPlan{DB:db,PolicySHA256:hash,File:f,FileIdentity:identity,DestinationIdentity:destinations,Naming:req,Preview:preview,Job:job,Client:client,Media:&media},nil
}

// Bind the caller's read-only preview to the exact current plan at both
// publication boundaries, including after the synced attempt marker.
// No callback can authorize a different plan. Refusal after the marker
// retains uncertain state and never retries the link. Existing isolated
// diagnostic entry points are not mounted in production and are unchanged.
func(a *Allocator)planNewPublication(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(newPublicationPlan,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.newPublicationPlanLocked(ctx,grant,g,naming,extras,false)
}
func(a *Allocator)publishPlannedNew(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,expected newPublicationPlan)([]placement,error){
	a.mu.Lock();defer a.mu.Unlock()
	raw,err:=encodePlannedAttempt(expected)
	if err!=nil{return nil,ErrStageRefused}
	guard:=func()error{
		current,err:=a.newPublicationPlanLocked(ctx,grant,g,naming,extras,false)
		if err!=nil{return err}
		again,err:=encodePlannedAttempt(current)
		if err!=nil||string(raw)!=string(again){return ErrStageRefused}
		return nil
	}
	return a.publishPreparedLocked(ctx,grant,raw,guard)
}

// Reobserve the stored plan with the actual published link and fresh
// naming/job authority before the landing attempt and after its durable
// marker. The legacy diagnostic landing cannot consume a guarded marker.
func(a *Allocator)landPlannedNew(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	_,_,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	plan,hash,err:=readPublicationAttempt(p,grant)
	p.Close();src.Close();dst.Close()
	if err!=nil||plan==nil{return ErrStageRefused}
	guard:=func()error{
		current,err:=a.newPublicationPlanLocked(ctx,grant,g,naming,extras,true)
		if err!=nil{return err}
		if !samePublicationPlan(*plan,current){return ErrStageRefused}
		return nil
	}
	if err:=guard();err!=nil{return err}
	return a.landNewPreparedLocked(ctx,grant,g,hash,guard)
}
