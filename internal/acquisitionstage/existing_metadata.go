package acquisitionstage

import(
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// Guarded existing-image metadata, first add-only source case. Existing
// same-site refetches and relationship/collection claims remain refused.
// This phase does not authorize alias disposal, naming, terminal receipt,
// or reclamation. All partial service writes retain their attempt.
const existingMetadataAttemptName="existing-metadata-attempt.json"
const existingMetadataResultName="existing-metadata-result.json"
const maxExistingMetadataAttemptBytes=64<<10

type existingMetadataAttempt struct{
	Repeat bool `json:",omitempty"`
	Version int
	LandingSHA256 string
	PolicySHA256 string
	Target metadataTarget
	Source string
	URL string
	ManagedPathID int64
	PreservedSHA256 string
}
type existingMetadataResult struct{
	Version int
	AttemptSHA256 string
	Row metadataObservation
	StateSHA256 string
}

func readExistingMetadataAttempt(p *os.Root,plan existingTargetPlan,link existingLinkAttempt)(existingMetadataAttempt,error){
	var m existingMetadataAttempt
	raw,err:=boundedRegularBytes(p,existingMetadataAttemptName,maxExistingMetadataAttemptBytes)
	hash,hashErr:=existingLandingDigest(link)
	if err!=nil||hashErr!=nil||json.Unmarshal(raw,&m)!=nil||m.Version!=1||m.LandingSHA256!=hash||
		m.PolicySHA256!=plan.Target.PolicySHA256||m.Target.ImageID!=plan.Target.ImageID||m.Target.PostID!=plan.Target.File.ID||
		!canonicalRating(m.Target.PriorRating)||len(m.Target.DerivedTags)!=0||m.Source==""||len(m.Source)>4096||m.URL==""||len(m.URL)>4096||
		m.ManagedPathID<=0||!validDigest(m.PreservedSHA256){return existingMetadataAttempt{},ErrStageRecovery}
	if plan.Target.Restore!=nil&&m.Target.PriorRating!=plan.Target.Restore.RestoreRating{return existingMetadataAttempt{},ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return existingMetadataAttempt{},ErrStageRecovery}
	return m,nil
}
func readExistingMetadataResult(p *os.Root,m existingMetadataAttempt)(existingMetadataResult,error){
	var v existingMetadataResult
	raw,err:=boundedRegularBytes(p,existingMetadataResultName,maxMetadataResultBytes)
	a,aerr:=json.Marshal(m)
	if err!=nil||aerr!=nil||json.Unmarshal(raw,&v)!=nil||v.Version!=1||v.AttemptSHA256!=digestBytes(a)||
		v.Row.ImageID!=m.Target.ImageID||v.Row.PostID!=m.Target.PostID||!canonicalRating(v.Row.Rating)||
		(m.Target.PriorRating!=""&&v.Row.Rating!=m.Target.PriorRating)||v.Row.Rejected==nil||v.Row.TagIDs==nil||!validDigest(v.StateSHA256){return existingMetadataResult{},ErrStageRecovery}
	seen:=map[string]bool{}
	for _,name:=range v.Row.Rejected{if name==""||seen[name]{return existingMetadataResult{},ErrStageRecovery};seen[name]=true}
	var prior int64
	for _,id:=range v.Row.TagIDs{if id<=prior{return existingMetadataResult{},ErrStageRecovery};prior=id}
	canonical,err:=json.Marshal(v)
	if err!=nil||!bytes.Equal(raw,canonical){return existingMetadataResult{},ErrStageRecovery}
	return v,nil
}

// The incoming site must be new to this image. Protect prior memberships,
// manual and other-site annotations, all other attribution ledger rows,
// original operator state and the exact post-landing path rows/relations.
// Source mirrors and newly derived tag memberships may legitimately change.
func existingPreservedState(ctx context.Context,g *library.Gallery,id int64,source string)(string,error){
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return "",ErrStageRecovery}
	defer tx.Rollback()
	operator,err:=restorePreservationDigest(ctx,tx,id)
	if err!=nil{return "",err}
	rest,err:=boundedStateDigest(ctx,tx,[]stateQuery{
		{`SELECT * FROM image_sources WHERE image_id=? AND site<>? ORDER BY site COLLATE BINARY,post_id COLLATE BINARY LIMIT 8193`,[]any{id,source}},
		{`SELECT * FROM image_annotations WHERE image_id=? AND (manual=1 OR site<>?) ORDER BY id LIMIT 8193`,[]any{id,source}},
		{`SELECT ts.*, it.tag_id FROM image_tag_sources ts LEFT JOIN image_tags it ON it.image_id=ts.image_id AND it.tag_id=ts.tag_id WHERE ts.image_id=? AND ts.source<>? ORDER BY ts.tag_id,ts.source COLLATE BINARY LIMIT 8193`,[]any{id,source}},
		{`SELECT * FROM image_paths WHERE image_id=? ORDER BY id LIMIT 8193`,[]any{id}},
		{`SELECT * FROM derivative_edges WHERE source_image_id=? OR derivative_image_id=? ORDER BY source_image_id,derivative_image_id LIMIT 8193`,[]any{id,id}},
	},nil)
	if err!=nil||tx.Commit()!=nil||ctx.Err()!=nil{return "",ErrStageRecovery}
	return digestBytes([]byte(operator+rest)),nil
}

func existingMetadataSourceEligible(ctx context.Context,g *library.Gallery,id int64,row acquisitionplan.ApprovedPolicyRow,allowRepeat bool)(bool,error){
	if allowRepeat{
		var exact int
		if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_sources WHERE image_id=? AND site=? AND post_id=? AND url=?`,id,row.SourceLabel,row.PostID,row.PostURL).Scan(&exact);err!=nil{return false,err}
		if exact==1{
			return true,nil
		}
	}
	for _,q:=range []struct{sql string;args []any}{
		{`SELECT COUNT(*) FROM image_sources WHERE (image_id=? AND site=?) OR (site=? AND post_id=?) OR url=? OR parent_url=?`,[]any{id,row.SourceLabel,row.SourceLabel,row.PostID,row.PostURL,row.PostURL}},
		{`SELECT COUNT(*) FROM image_tag_sources WHERE image_id=? AND source=?`,[]any{id,row.SourceLabel}},
		{`SELECT COUNT(*) FROM image_annotations WHERE image_id=? AND site=? AND manual=0`,[]any{id,row.SourceLabel}},
	}{
		var n int
		if err:=g.DB.Read.QueryRowContext(ctx,q.sql,q.args...).Scan(&n);err!=nil||n!=0{return false,ErrStageRefused}
	}
	return false,nil
}

// For this new-site-only case require even omitted source claims to be
// empty and the complete source attribution ledger to match the result.
func verifyExistingMappedRow(ctx context.Context,g *library.Gallery,m existingMetadataAttempt,row acquisitionplan.ApprovedPolicyRow,rejected []string)(metadataObservation,error){
	var zero metadataObservation
	if m.Repeat {
		// A repeat has already verified the incoming bytes and evaluated
		// current source policy. It keeps the existing source and tags.
		// Fresh mutable claims do not describe a write by this operation.
		exact,err:=existingMetadataSourceEligible(ctx,g,m.Target.ImageID,row,true)
		if err!=nil||!exact{return zero,fmt.Errorf("%w: existing source changed",ErrStageRecovery)}
		rating,err:=readImageRating(ctx,g,m.Target.ImageID)
		if err!=nil||rating!=m.Target.PriorRating{return zero,fmt.Errorf("%w: retained rating changed",ErrStageRecovery)}
		return metadataObservation{ImageID:m.Target.ImageID,PostID:m.Target.PostID,Rating:rating,TagIDs:[]int64{},Rejected:[]string{}},nil
	}
	v,err:=verifyMappedRow(ctx,g,m.Target,row,rejected)
	if err!=nil{return zero,err}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return zero,ErrStageRecovery}
	defer tx.Rollback()
	in,md5,err:=normalizedMappedSource(row)
	if err!=nil||verifySourceClaimsMode(ctx,tx,v.ImageID,in,md5,true)!=nil{return zero,ErrStageRecovery}
	for _,q:=range []struct{sql string;args []any;want int}{
		{`SELECT COUNT(*) FROM image_sources WHERE image_id=? AND site=?`,[]any{v.ImageID,row.SourceLabel},1},
		{`SELECT COUNT(*) FROM image_sources WHERE (site=? AND post_id=?) OR url=?`,[]any{row.SourceLabel,row.PostID,row.PostURL},1},
		{`SELECT COUNT(*) FROM image_tag_sources WHERE image_id=? AND source=?`,[]any{v.ImageID,row.SourceLabel},len(v.TagIDs)},
		{`SELECT COUNT(*) FROM image_tag_sources ts JOIN image_tags it ON it.image_id=ts.image_id AND it.tag_id=ts.tag_id WHERE ts.image_id=? AND ts.source=? AND it.stale=0 AND it.is_implied=0`,[]any{v.ImageID,row.SourceLabel},len(v.TagIDs)},
	}{
		var n int
		if err:=tx.QueryRowContext(ctx,q.sql,q.args...).Scan(&n);err!=nil||n!=q.want{return zero,ErrStageRecovery}
	}
	if tx.Commit()!=nil||ctx.Err()!=nil{return zero,ErrStageRecovery}
	return v,nil
}

type existingMetadataEvidence struct{
	Attempt existingMetadataAttempt
	Row acquisitionplan.ApprovedPolicyRow
	StateSHA256 string
}

// Each call verifies the original guarded landing records and current
// authority. The pristine form additionally requires an entirely fresh
// source and exact pre-merge state. Post-merge form is nonterminal and
// only accepts independently verified source claims and preservation.
func(a *Allocator) existingMetadataEvidenceLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority,prior *existingMetadataAttempt)(existingMetadataEvidence,error){
	var zero existingMetadataEvidence
	landed,err:=a.observeExistingLandingModeLocked(ctx,grant,g,disposition,extras,naming,prior==nil)
	if err!=nil{return zero,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRecovery}
	plan,err:=readExistingTargetPlan(p,r,e.Files[0]);if err!=nil{return zero,err}
	link,err:=readExistingLinkAttempt(p,r,plan);if err!=nil||readExistingLandingAttempt(p,link)!=nil{return zero,ErrStageRecovery}
	hash,err:=existingLandingDigest(link);if err!=nil{return zero,err}
	snapshot,err:=a.boundPolicyLocked(ctx,grant);if err!=nil{return zero,err}
	_,policy,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	rows,rowErr:=newOnlyRows(e,snapshot)
	if err!=nil||rowErr!=nil||policy!=plan.Target.PolicySHA256||len(rows)!=1{return zero,ErrStageRecovery}
	row:=rows[0]
	id:=landed.ImageID
	m:=existingMetadataAttempt{Version:1,LandingSHA256:hash,PolicySHA256:policy,Target:metadataTarget{PostID:landed.PostID,ImageID:id},Source:row.SourceLabel,URL:row.PostURL}
	if err:=g.DB.Read.QueryRowContext(ctx,`SELECT id FROM image_paths WHERE image_id=? AND path=?`,id,filepath.Join(r.GalleryRoot,finalName(grant,e.Files[0]))).Scan(&m.ManagedPathID);err!=nil||m.ManagedPathID<=0{return zero,ErrStageRecovery}
	m.PreservedSHA256,err=existingPreservedState(ctx,g,id,row.SourceLabel)
	if err!=nil{return zero,err}
	if prior==nil{
		m.Repeat,err=existingMetadataSourceEligible(ctx,g,id,row,plan.Target.Kind==existingHeld)
		if err!=nil{return zero,err}
		m.Target.PriorRating,err=readImageRating(ctx,g,id)
		if err!=nil{return zero,err}
	}else{
		m.Target.PriorRating=prior.Target.PriorRating
		m.Repeat=prior.Repeat
		if !reflect.DeepEqual(m,*prior){return zero,ErrStageRecovery}
		// No relationship write is necessary or authorized for this scope.
		graph,err:=inspectRelationships(ctx,g,id,row.PostURL,"")
		if err!=nil||graph.Parent!=0||len(graph.Children)!=0||len(graph.Edges)!=0{return zero,ErrStageRecovery}
	}
	var state string
	if plan.Target.Kind==existingHeld{
		state,_,err=retainedHeldState(ctx,g,r,dst,e.Files[0],*plan.Target.Held)
	}else{state,err=restoreStateDigest(ctx,g,id)}
	if err!=nil||!validDigest(state){return zero,ErrStageRecovery}
	if !a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
	return existingMetadataEvidence{Attempt:m,Row:row,StateSHA256:state},nil
}

func(a *Allocator) applyExistingMetadata(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)(metadataObservation,error){
	a.mu.Lock();defer a.mu.Unlock()
	var zero metadataObservation
	before,err:=a.existingMetadataEvidenceLocked(ctx,grant,g,disposition,extras,naming,nil)
	if err!=nil{return zero,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	for _,name:=range []string{existingMetadataAttemptName,existingMetadataResultName}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	}
	estimate:=8192
	for _,name:=range before.Row.Tags{
		b,err:=json.Marshal(name);if err!=nil{return zero,err}
		estimate+=len(b)+24
		if estimate>maxMetadataResultBytes{return zero,ErrStageLimit}
	}
	raw,err:=json.Marshal(before.Attempt)
	if err!=nil||len(raw)>maxExistingMetadataAttemptBytes{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,existingMetadataAttemptName,raw);err!=nil{return zero,err}
	syncDirectory(p)
	current,err:=a.existingMetadataEvidenceLocked(ctx,grant,g,disposition,extras,naming,nil)
	if err!=nil||!reflect.DeepEqual(before,current)||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
	plan,err:=readExistingTargetPlan(p,r,e.Files[0]);if err!=nil{return zero,err}
	link,err:=readExistingLinkAttempt(p,r,plan);if err!=nil{return zero,err}
	stored,err:=readExistingMetadataAttempt(p,plan,link)
	if err!=nil||!reflect.DeepEqual(stored,before.Attempt){return zero,ErrStageRecovery}
	row:=before.Row
	input,md5,err:=normalizedMappedSource(row)
	if err!=nil{return zero,err}
	var sum gallery.MergeSummary
	var warnings []string
	if !stored.Repeat{
		err=g.Edit(func(d *db.DB)error{
		var err error
		sum,warnings,err=gallery.MergeSource(d,g.TagSvc,stored.Target.ImageID,input.Source,input.PostID,input.URL,md5,input.ParentURL,input.PostFile,row.Tags,input.Withheld)
		if err!=nil||len(warnings)!=0{return err}
		_,err=gallery.ApplySourceProvenance(d,stored.Target.ImageID,input.Source,input.PostID,input.Commentary,input.Translated,input.Original,input.Notes)
		return err
		})
		if err!=nil||len(warnings)!=0||!sum.SourceAdded||sum.TagsRetired!=0{return zero,ErrStageRecovery}
	}
	after,err:=a.existingMetadataEvidenceLocked(ctx,grant,g,disposition,extras,naming,&stored)
	if err!=nil{return zero,err}
	v,err:=verifyExistingMappedRow(ctx,g,stored,after.Row,sum.RejectedTags)
	if err!=nil{return zero,err}
	again,err:=a.existingMetadataEvidenceLocked(ctx,grant,g,disposition,extras,naming,&stored)
	if err!=nil||!reflect.DeepEqual(after,again){return zero,ErrStageRecovery}
	result:=existingMetadataResult{Version:1,AttemptSHA256:digestBytes(raw),Row:v,StateSHA256:again.StateSHA256}
	body,err:=json.Marshal(result)
	if err!=nil||len(body)>maxMetadataResultBytes{return zero,ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,existingMetadataResultName,body);err!=nil{return zero,err}
	syncDirectory(p)
	return a.observeExistingMetadataLocked(ctx,grant,g,disposition,extras,naming)
}

func(a *Allocator) observeExistingMetadata(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)(metadataObservation,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeExistingMetadataLocked(ctx,grant,g,disposition,extras,naming)
}
func(a *Allocator) observeExistingMetadataLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority,naming namingAuthority)(metadataObservation,error){
	var zero metadataObservation
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRecovery}
	plan,err:=readExistingTargetPlan(p,r,e.Files[0]);if err!=nil{return zero,err}
	link,err:=readExistingLinkAttempt(p,r,plan);if err!=nil||readExistingLandingAttempt(p,link)!=nil{return zero,ErrStageRecovery}
	m,err:=readExistingMetadataAttempt(p,plan,link);if err!=nil{return zero,err}
	result,err:=readExistingMetadataResult(p,m);if err!=nil{return zero,err}
	for i:=0;i<2;i++{
		current,err:=a.existingMetadataEvidenceLocked(ctx,grant,g,disposition,extras,naming,&m)
		if err!=nil||current.StateSHA256!=result.StateSHA256{return zero,ErrStageRecovery}
		v,err:=verifyExistingMappedRow(ctx,g,m,current.Row,result.Row.Rejected)
		if err!=nil||!sameMetadataObservation(v,result.Row){return zero,ErrStageRecovery}
	}
	again,err:=readExistingMetadataAttempt(p,plan,link)
	if err!=nil||!reflect.DeepEqual(m,again){return zero,ErrStageRecovery}
	observed,err:=readExistingMetadataResult(p,m)
	if err!=nil||!reflect.DeepEqual(result,observed)||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRecovery}
	return result.Row,nil
}
