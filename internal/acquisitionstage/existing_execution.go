package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// Isolated actual link and ingestion. Separate records deliberately
// cannot authorize the legacy metadata or terminal receipt pipeline.
// Never retry a link or ingest after its exclusive attempt exists.
const existingLinkAttemptName = "existing-link-attempt.json"
const existingLandingAttemptName = "existing-landing-attempt"
const maxExistingLinkAttemptBytes = 64 << 10
const maxExistingLandingAttemptBytes = 64

type existingLinkAttempt struct {
	Version int
	TargetSHA256 string
	Naming *gallery.ReceivedNamingRequest
	Preview *gallery.ReceivedNamingPreview
	DestinationIdentity [2]string
}

func validExistingLinkAttempt(m existingLinkAttempt,r record,p existingTargetPlan)bool{
	raw,err:=encodeExistingTargetPlan(p,r,p.Target.File)
	if err!=nil||m.Version!=1||m.TargetSHA256!=digestBytes(raw){return false}
	if p.Target.Kind==existingHeld{return m.Naming==nil&&m.Preview==nil&&m.DestinationIdentity==[2]string{}}
	if p.Target.Kind!=existingRestore||m.Naming==nil||m.Preview==nil||p.Target.Restore==nil{return false}
	q:=*m.Naming
	if !validNamingRequest(q)||q.Gallery!=r.Allocation.Request.Transfer.Metadata.Gallery||q.ReceivedName!=p.Target.File.RelativeName||q.PostID!=p.Target.File.ID{return false}
	for i,path:=range []string{m.Preview.InitialPath,m.Preview.FinalPath}{
		rel,err:=namingRelative(r.GalleryRoot,path)
		if err!=nil||strings.EqualFold(rel,finalName(r.Allocation,p.Target.File))||strings.EqualFold(rel,p.Target.Restore.RestorePath)||
			m.DestinationIdentity[i]==""||len(m.DestinationIdentity[i])>128{return false}
	}
	return true
}
func encodeExistingLinkAttempt(m existingLinkAttempt,r record,p existingTargetPlan)([]byte,error){
	if !validExistingLinkAttempt(m,r,p){return nil,ErrStageRefused}
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxExistingLinkAttemptBytes{return nil,ErrStageLimit}
	return raw,nil
}
func readExistingLinkAttempt(root *os.Root,r record,p existingTargetPlan)(existingLinkAttempt,error){
	var m existingLinkAttempt
	raw,err:=boundedRegularBytes(root,existingLinkAttemptName,maxExistingLinkAttemptBytes)
	if err!=nil||json.Unmarshal(raw,&m)!=nil{return existingLinkAttempt{},ErrStageRecovery}
	canonical,err:=encodeExistingLinkAttempt(m,r,p)
	if err!=nil||!bytes.Equal(raw,canonical){return existingLinkAttempt{},ErrStageRecovery}
	return m,nil
}
func existingLandingDigest(m existingLinkAttempt)(string,error){
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxExistingLinkAttemptBytes{return "",ErrStageRecovery}
	return digestBytes(raw),nil
}
func readExistingLandingAttempt(p *os.Root,m existingLinkAttempt)error{
	want,err:=existingLandingDigest(m)
	raw,readErr:=boundedRegularBytes(p,existingLandingAttemptName,maxExistingLandingAttemptBytes)
	if err!=nil||readErr!=nil||string(raw)!=want{return ErrStageRecovery}
	return nil
}

// Held duplicates do not rename existing files. For restoration bind
// the same conservative measured naming preview as new acquisition,
// rather than accepting a later arbitrary rename. Unsupported names
// or missing destination directories refuse before linking.
func(a *Allocator) existingLinkEvidenceLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority,phase string)(existingLinkAttempt,existingTargetPlan,error){
	var zero existingLinkAttempt
	var noPlan existingTargetPlan
	if g==nil||g.WatcherRunning(){return zero,noPlan,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,noPlan,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,noPlan,ErrStageRefused}
	plan,err:=readExistingTargetPlan(p,r,e.Files[0])
	if err!=nil{return zero,noPlan,err}
	raw,err:=encodeExistingTargetPlan(plan,r,e.Files[0])
	if err!=nil{return zero,noPlan,err}
	m:=existingLinkAttempt{Version:1,TargetSHA256:digestBytes(raw)}
	if plan.Target.Kind==existingRestore{
		if naming==nil{return zero,noPlan,ErrStageRefused}
		req,err:=naming(ctx,grant)
		if err!=nil{return zero,noPlan,ErrStageRefused}
		snapshot,err:=a.boundPolicyLocked(ctx,grant)
		if err!=nil{return zero,noPlan,err}
		rows,err:=newOnlyRows(e,snapshot)
		if err!=nil||len(rows)!=1||req.Source!=rows[0].SourceLabel||req.PostID!=rows[0].PostID{return zero,noPlan,ErrStageRefused}
		v:=plan.Target.Media
		preview,err:=gallery.PreviewMeasuredReceivedNaming(ctx,g.DB,g.Boundary(),req,e.Files[0].SHA256,e.Files[0].Size,v.Format,v.Width,v.Height,v.MD5)
		if err!=nil{return zero,noPlan,ErrStageRefused}
		m.Naming,m.Preview=&req,&preview
		for i,path:=range []string{preview.InitialPath,preview.FinalPath}{
			rel,err:=namingRelative(r.GalleryRoot,path)
			if err!=nil{return zero,noPlan,ErrStageRefused}
			parent,_,err:=namingParent(dst,rel)
			if err!=nil{return zero,noPlan,err}
			live:=namingParentLive(r,dst,parent,rel)
			m.DestinationIdentity[i],err=rootIdentity(parent)
			parent.Close()
			if err!=nil||!live{return zero,noPlan,ErrStageRefused}
		}
	}
	if !validExistingLinkAttempt(m,r,plan){return zero,noPlan,ErrStageRefused}
	current,err:=a.inspectExistingPublicationTargetModeLocked(ctx,grant,g,plan.Target.Kind,existingPlanAuthority(plan.Target.Kind,disposition,extras),phase)
	if err!=nil||!sameExistingPublicationTarget(plan.Target,current){return zero,noPlan,ErrStageRefused}
	again,err:=readExistingTargetPlan(p,r,e.Files[0])
	if err!=nil||!sameExistingPublicationTarget(plan.Target,again.Target)||!a.livePublicationRoots(grant,r,p,src,dst){return zero,noPlan,ErrStageRecovery}
	// Reobserve naming after the authority and target reads. No callback
	// may silently substitute a different request or destination.
	if m.Naming!=nil{
		req,err:=naming(ctx,grant)
		if err!=nil||req!=*m.Naming{return zero,noPlan,ErrStageRefused}
		v:=plan.Target.Media
		preview,err:=gallery.PreviewMeasuredReceivedNaming(ctx,g.DB,g.Boundary(),req,e.Files[0].SHA256,e.Files[0].Size,v.Format,v.Width,v.Height,v.MD5)
		if err!=nil||preview!=*m.Preview{return zero,noPlan,ErrStageRefused}
		check:=newPublicationPlan{File:e.Files[0],Preview:preview,DestinationIdentity:m.DestinationIdentity}
		if err:=verifyPlanDestinations(r,dst,&check);err!=nil{return zero,noPlan,err}
	}
	current,err=a.inspectExistingPublicationTargetModeLocked(ctx,grant,g,plan.Target.Kind,existingPlanAuthority(plan.Target.Kind,disposition,extras),phase)
	if err!=nil||!sameExistingPublicationTarget(plan.Target,current){return zero,noPlan,ErrStageRefused}
	return m,plan,nil
}

func(a *Allocator) publishExistingTarget(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	m,plan,err:=a.existingLinkEvidenceLocked(ctx,grant,g,disposition,extras,naming,"prepared")
	if err!=nil{return err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return ErrStageRefused}
	for _,name:=range []string{existingLinkAttemptName,existingLandingAttemptName,"attempt"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	common,from,to,err:=commonRoot(filepath.Join(a.path,grant.Request.OperationID,"publication","media"),r.GalleryRoot)
	if err!=nil{return err}
	defer common.Close()
	rootsMatch:=func()bool{
		si,err:=src.Stat(".");if err!=nil{return false}
		di,err:=dst.Stat(".");if err!=nil{return false}
		x,err:=common.Lstat(from);if err!=nil||!x.IsDir()||!os.SameFile(si,x){return false}
		y,err:=common.Lstat(to);return err==nil&&y.IsDir()&&os.SameFile(di,y)&&a.livePublicationRoots(grant,r,p,src,dst)
	}
	raw,err:=encodeExistingLinkAttempt(m,r,plan)
	if err!=nil||!rootsMatch(){return ErrStageRefused}
	if err:=writeOwnerBytes(ctx,p,existingLinkAttemptName,raw);err!=nil{return err}
	syncDirectory(p)
	guard,observed,err:=a.existingLinkEvidenceLocked(ctx,grant,g,disposition,extras,naming,"absent")
	if err!=nil||!reflect.DeepEqual(m,guard)||!sameExistingPublicationTarget(plan.Target,observed.Target)||!rootsMatch(){return ErrStageRefused}
	stored,err:=readExistingLinkAttempt(p,r,plan)
	if err!=nil||!reflect.DeepEqual(stored,m){return ErrStageRecovery}
	f:=e.Files[0]
	if err:=common.Link(filepath.Join(from,f.RelativeName),filepath.Join(to,finalName(grant,f)));err!=nil{return ErrStageRecovery}
	syncDirectory(dst)
	guard,observed,err=a.existingLinkEvidenceLocked(ctx,grant,g,disposition,extras,naming,"placed")
	if err!=nil||!reflect.DeepEqual(m,guard)||!sameExistingPublicationTarget(plan.Target,observed.Target)||!rootsMatch(){return ErrStageRecovery}
	return nil
}

// A live post-ingestion witness, not a receipt. Held state is unchanged
// except for the newly managed alias row; restored state preserves the
// original ID, rating, operator-owned fields and prior path semantics.
func(a *Allocator) observeExistingLandingLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)(landingObservation,error){
	var zero landingObservation
	if g==nil||g.WatcherRunning(){return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRecovery}
	plan,err:=readExistingTargetPlan(p,r,e.Files[0])
	if err!=nil{return zero,err}
	m,err:=readExistingLinkAttempt(p,r,plan)
	if err!=nil||readExistingLandingAttempt(p,m)!=nil{return zero,ErrStageRecovery}
	auth:=existingPlanAuthority(plan.Target.Kind,disposition,extras)
	if auth==nil{return zero,ErrStageRefused}
	observe:=func()(landingObservation,error){
		job,err:=auth(ctx,grant)
		if err!=nil||!job.Empty(){return zero,ErrStageRefused}
		current,err:=observedDB(r,g)
		if err!=nil||current!=plan.Target.DB{return zero,ErrStageRecovery}
		snapshot,err:=a.boundPolicyLocked(ctx,grant)
		if err!=nil{return zero,err}
		_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
		if err!=nil||hash!=plan.Target.PolicySHA256{return zero,ErrStageRecovery}
		places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
		if err!=nil||len(places)!=1||places[0].State!="placed"{return zero,ErrStageRecovery}
		f:=e.Files[0]
		identity,err:=namingFile(ctx,src,f.RelativeName,f,2)
		if err!=nil||identity!=plan.Target.FileIdentity||verifyPlannedImageRow(ctx,g,plan.Target.ImageID,f,&plan.Target.Media)!=nil{return zero,ErrStageRecovery}
		rows,err:=readLandingRows(ctx,g,r,e)
		if err!=nil||len(rows)!=1||rows[0].State!="observed_path"||rows[0].ImageID!=plan.Target.ImageID{return zero,ErrStageRecovery}
		row:=rows[0]
		switch plan.Target.Kind{
		case existingHeld:
			if row.Canonical||plan.Target.Held==nil{return zero,ErrStageRecovery}
			state,retained,err:=retainedHeldState(ctx,g,r,dst,f,*plan.Target.Held)
			if err!=nil||state!=plan.Target.StateSHA256||!reflect.DeepEqual(retained,plan.Target.Retained){return zero,ErrStageRecovery}
		case existingRestore:
			if !row.Canonical||plan.Target.Restore==nil||m.Naming==nil||naming==nil{return zero,ErrStageRecovery}
			if err:=verifyRestoredLanding(ctx,g,r,e,*plan.Target.Restore,dst,row);err!=nil{return zero,err}
			rating,err:=readImageRating(ctx,g,row.ImageID)
			if err!=nil||rating!=plan.Target.Restore.RestoreRating{return zero,ErrStageRecovery}
			req,err:=naming(ctx,grant)
			if err!=nil||req!=*m.Naming{return zero,ErrStageRefused}
			preview,err:=gallery.PreviewReceivedNaming(ctx,g.DB,g.Boundary(),row.ImageID,req)
			if err!=nil||preview!=*m.Preview{return zero,ErrStageRecovery}
			check:=newPublicationPlan{File:f,Preview:preview,DestinationIdentity:m.DestinationIdentity}
			if err:=verifyPlanDestinations(r,dst,&check);err!=nil{return zero,err}
		default:return zero,ErrStageRecovery
		}
		s,err:=a.scope(ctx,grant.Request)
		if err!=nil||!matches(r,s)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
		return row,nil
	}
	first,err:=observe();if err!=nil{return zero,err}
	second,err:=observe();if err!=nil||first!=second{return zero,ErrStageRecovery}
	return second,nil
}

func(a *Allocator) observeExistingLanding(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)(landingObservation,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeExistingLandingLocked(ctx,grant,g,disposition,extras,naming)
}

func(a *Allocator) landExistingTarget(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	m,plan,err:=a.existingLinkEvidenceLocked(ctx,grant,g,disposition,extras,naming,"placed")
	if err!=nil{return err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return ErrStageRecovery}
	stored,err:=readExistingLinkAttempt(p,r,plan)
	if err!=nil||!reflect.DeepEqual(stored,m){return ErrStageRecovery}
	if _,err:=p.Lstat(existingLandingAttemptName);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	hash,err:=existingLandingDigest(m)
	if err!=nil{return err}
	if err:=writeOwnerBytes(ctx,p,existingLandingAttemptName,[]byte(hash));err!=nil{return err}
	syncDirectory(p)
	guard,current,err:=a.existingLinkEvidenceLocked(ctx,grant,g,disposition,extras,naming,"placed")
	if err!=nil||!reflect.DeepEqual(m,guard)||!sameExistingPublicationTarget(plan.Target,current.Target)||readExistingLandingAttempt(p,m)!=nil{return ErrStageRefused}
	f:=e.Files[0]
	if err:=readCheckedLinks(ctx,dst,finalName(grant,f),f.Size,f.SHA256,io.Discard,2);err!=nil{return err}
	res,err:=g.LandInPlace(ctx,filepath.Join(r.GalleryRoot,finalName(grant,f)),library.LandSpec{Origin:newReceiptOrigin,MaxBytes:f.Size})
	if err!=nil||res.Image==nil||res.Dropped||res.Image.ID!=plan.Target.ImageID||res.Duplicate!=(plan.Target.Kind==existingHeld){return ErrStageRecovery}
	_,err=a.observeExistingLandingLocked(ctx,grant,g,disposition,extras,naming)
	return err
}
