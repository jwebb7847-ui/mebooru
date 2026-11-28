package acquisitionstage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// Single-post, explicit source collection/order only. Not a production
// receipt. The caller must exclude other gallery writes and scanners.
// Zero order needs a separately bound legacy job index, not a guess.
const maxCollectionRecord = 512 << 10
const maxCollectionMembers = 256

func validCollectionClaim(p *acquisitionplan.ApprovedProvenance) bool {
	if p == nil { return true }
	if p.Collection == "" { return p.CollectionOrder == 0 }
	return validCollectionLabel(p.Collection) && p.CollectionOrder > 0
}

func validCollectionLabel(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= contract.MaxCollection
}

type collectionMember struct {
	Name string `json:"name"`
	Position *int64 `json:"position"`
}
type collectionState struct {
	Home string `json:"home"`
	Position *int64 `json:"position"`
	Members []collectionMember `json:"members"`
}
type collectionSnapshot struct {
	Label string `json:"label"`
	Member string `json:"member"`
	State collectionState `json:"state"`
}
type collectionAttempt struct {
	Version int `json:"version"`
	MetadataSHA256 string `json:"metadata_sha256"`
	RelationshipSHA256 string `json:"relationship_sha256"`
	ImageID int64 `json:"image_id"`
	Name string `json:"name"`
	Order int `json:"order"`
	NewOnly bool `json:"new_only"`
	Before collectionSnapshot `json:"before"`
}
type collectionResult struct {
	Version int `json:"version"`
	AttemptSHA256 string `json:"attempt_sha256"`
	After collectionSnapshot `json:"after"`
}

func nullablePosition(n sql.NullInt64) *int64 {
	if !n.Valid { return nil }
	v := n.Int64
	return &v
}
func samePosition(a,b *int64) bool {
	return (a==nil && b==nil) || (a!=nil && b!=nil && *a==*b)
}
func validCollectionState(s collectionState) bool {
	if s.Members==nil || len(s.Members)>maxCollectionMembers || (s.Position!=nil && *s.Position<1) { return false }
	home:=0
	for i,m:=range s.Members {
		if !validCollectionLabel(m.Name) || (m.Position!=nil && *m.Position<1) ||
			(i>0 && s.Members[i-1].Name>=m.Name) { return false }
		if strings.EqualFold(m.Name,s.Home) {
			if !samePosition(m.Position,s.Position) { return false }
			home++
		}
	}
	if s.Home=="" { return len(s.Members)==0 && s.Position==nil }
	return validCollectionLabel(s.Home) && home==1
}
func validCollectionSnapshot(s collectionSnapshot,name string) bool {
	if !validCollectionLabel(s.Label) || !validCollectionLabel(s.Member) ||
		!strings.EqualFold(s.Label,name) || !strings.EqualFold(s.Member,s.Label) ||
		!validCollectionState(s.State) { return false }
	// Refuse nontrivial Unicode/case ambiguity rather than inventing a
	// different resolution from the library's SQLite NOCASE constraint.
	matches:=0
	for _,m:=range s.State.Members {
		if strings.EqualFold(m.Name,s.Label) {
			if m.Name!=s.Member { return false }
			matches++
		}
	}
	return matches<=1 && (matches==1 || s.Member==s.Label)
}
func inspectCollection(ctx context.Context,g *library.Gallery,id int64,name string)(collectionSnapshot,error){
	var out collectionSnapshot
	if g==nil || id<=0 || !validCollectionLabel(name) { return out,ErrStageRefused }
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil { return out,ErrStageRecovery }
	defer tx.Rollback()
	out.Label,err=gallery.ExistingLabelTx(tx,name)
	if err!=nil { return out,ErrStageRecovery }
	err=tx.QueryRowContext(ctx,`SELECT name FROM image_collections WHERE image_id=? AND name=?`,id,out.Label).Scan(&out.Member)
	if errors.Is(err,sql.ErrNoRows) { out.Member=out.Label } else if err!=nil { return out,ErrStageRecovery }
	var home sql.NullString
	var pos sql.NullInt64
	if err:=tx.QueryRowContext(ctx,`SELECT series,series_order FROM images WHERE id=?`,id).Scan(&home,&pos);err!=nil { return out,ErrStageRecovery }
	out.State.Home=home.String
	out.State.Position=nullablePosition(pos)
	out.State.Members=make([]collectionMember,0)
	rows,err:=tx.QueryContext(ctx,`SELECT name,position FROM image_collections WHERE image_id=? ORDER BY name COLLATE BINARY LIMIT ?`,id,maxCollectionMembers+1)
	if err!=nil { return out,ErrStageRecovery }
	for rows.Next() {
		var m collectionMember
		var p sql.NullInt64
		if rows.Scan(&m.Name,&p)!=nil || len(out.State.Members)>=maxCollectionMembers { rows.Close(); return out,ErrStageRecovery }
		m.Position=nullablePosition(p)
		out.State.Members=append(out.State.Members,m)
	}
	err=rows.Err(); rows.Close()
	if err!=nil || !validCollectionSnapshot(out,name) || tx.Commit()!=nil || ctx.Err()!=nil { return out,ErrStageRecovery }
	return out,nil
}
func expectedCollection(m collectionAttempt)(collectionSnapshot,error){
	var zero collectionSnapshot
	if m.Version!=1 || m.ImageID<=0 || !validCollectionLabel(m.Name) || m.Order<1 ||
		!validCollectionSnapshot(m.Before,m.Name) { return zero,ErrStageRecovery }
	if m.NewOnly && (m.Before.State.Home!="" || len(m.Before.State.Members)!=0) { return zero,ErrStageRefused }
	out:=m.Before
	out.State.Members=append([]collectionMember{},m.Before.State.Members...)
	n:=int64(m.Order)
	found:=false
	for i,v:=range out.State.Members {
		if v.Name==out.Member { out.State.Members[i].Position=&n; found=true }
	}
	if !found { out.State.Members=append(out.State.Members,collectionMember{Name:out.Member,Position:&n}) }
	sort.Slice(out.State.Members,func(i,j int)bool{return out.State.Members[i].Name<out.State.Members[j].Name})
	if m.NewOnly || out.State.Home=="" { out.State.Home=out.Label;out.State.Position=&n
	} else if strings.EqualFold(out.State.Home,out.Label) { out.State.Position=&n }
	if !validCollectionSnapshot(out,m.Name) { return zero,ErrStageRecovery }
	return out,nil
}

func sameCollectionScope(a,b collectionAttempt)bool{
	return a.Version==b.Version && a.MetadataSHA256==b.MetadataSHA256 && a.RelationshipSHA256==b.RelationshipSHA256 &&
		a.ImageID==b.ImageID && a.Name==b.Name && a.Order==b.Order && a.NewOnly==b.NewOnly
}
func(a *Allocator)collectionContext(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)(collectionAttempt,*os.Root,error){
	var zero collectionAttempt
	if err:=a.observeRelationshipsLocked(ctx,grant,g);err!=nil { return zero,nil,err }
	rel,p,err:=a.relationshipContext(ctx,grant,g)
	if err!=nil { return zero,nil,err }
	fail:=func(err error)(collectionAttempt,*os.Root,error){p.Close();return zero,nil,err}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil || len(snapshot.Rows)!=1 { return fail(ErrStageRefused) }
	row:=snapshot.Rows[0]
	claim:=row.Provenance
	if claim==nil || claim.Collection=="" || !validCollectionClaim(claim) || row.PostURL!=rel.URL { return fail(ErrStageRefused) }
	r,err:=a.load(grant.Request.OperationID)
	if err!=nil { return fail(err) }
	l,err:=readLandingAttempt(p,r)
	if err!=nil || l.RestoreImageID!=0 { return fail(ErrStageRefused) }
	landed,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil || len(landed)!=1 || landed[0].State!="observed_path" || landed[0].ImageID!=rel.ImageID ||
		landed[0].PostID!=row.PostID || landed[0].Canonical!=l.NewOnly { return fail(ErrStageRecovery) }
	if !l.NewOnly {
		held,err:=a.observeHeldDuplicatesLocked(ctx,grant,g)
		if err!=nil || len(held)!=1 || held[0].ImageID!=rel.ImageID { return fail(ErrStageRecovery) }
	}
	for _,name:=range []string{"new-receipt.json","duplicate-attempt.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return fail(ErrStageRecovery)}
	}
	raw,err:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil { return fail(err) }
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil || !matches(r,s) || g.WatcherRunning() { return fail(ErrStageRefused) }
	current,err:=observedDB(r,g)
	if err!=nil || !sameLandingDB(current,l) { return fail(ErrStageRecovery) }
	plan,_,err:=readPublicationAttempt(p,grant)
	if err!=nil{return fail(err)}
	if plan!=nil{
		if !matchesCollectionClaim(plan.Collection,claim){return fail(ErrStageRefused)}
		observed,err:=inspectPlannedCollection(ctx,g,claim)
		if err!=nil||observed==nil||*observed!=*plan.Collection{return fail(ErrStageRefused)}
	}
	return collectionAttempt{Version:1,MetadataSHA256:rel.MetadataSHA256,RelationshipSHA256:digestBytes(raw),
		ImageID:rel.ImageID,Name:claim.Collection,Order:claim.CollectionOrder,NewOnly:l.NewOnly},p,nil
}
func(a *Allocator)applyCollections(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	a.mu.Lock();defer a.mu.Unlock()
	m,p,err:=a.collectionContext(ctx,grant,g)
	if err!=nil{return err}
	defer p.Close()
	for _,name:=range []string{"collection-attempt.json","collection-result.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	m.Before,err=inspectCollection(ctx,g,m.ImageID,m.Name)
	if err!=nil{return err}
	plan,_,err:=readPublicationAttempt(p,grant)
	if err!=nil{return err}
	if plan!=nil&&!matchesPlannedCollectionAttempt(plan.Collection,m){return ErrStageRefused}
	expected,err:=expectedCollection(m)
	if err!=nil{return err}
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxCollectionRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"collection-attempt.json",raw);err!=nil{return err}
	syncDirectory(p)
	current,check,err:=a.collectionContext(ctx,grant,g)
	if err!=nil{return err}
	sameRoot:=sameRelationshipRoot(p,check);check.Close()
	if !sameRoot||!sameCollectionScope(m,current){return ErrStageRecovery}
	before,err:=inspectCollection(ctx,g,m.ImageID,m.Name)
	if err!=nil||!reflect.DeepEqual(before,m.Before){return ErrStageRecovery}
	// Reuse the actual create versus duplicate collection policies.
	err=g.Edit(func(d *db.DB)error{
		if m.NewOnly { return gallery.SetHomeCollection(d,m.ImageID,m.Name,&m.Order) }
		return gallery.AddCollectionMembership(d,m.ImageID,m.Name,&m.Order)
	})
	if err!=nil{return ErrStageRecovery}
	after,err:=inspectCollection(ctx,g,m.ImageID,m.Name)
	if err!=nil||!reflect.DeepEqual(after,expected){return ErrStageRecovery}
	current,check,err=a.collectionContext(ctx,grant,g)
	if err!=nil{return err}
	sameRoot=sameRelationshipRoot(p,check);check.Close()
	if !sameRoot||!sameCollectionScope(m,current){return ErrStageRecovery}
	body,err:=json.Marshal(collectionResult{Version:1,AttemptSHA256:digestBytes(raw),After:after})
	if err!=nil||len(body)>maxCollectionRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"collection-result.json",body);err!=nil{return err}
	syncDirectory(p)
	return a.observeCollectionsLocked(ctx,grant,g)
}
func(a *Allocator)observeCollections(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeCollectionsLocked(ctx,grant,g)
}
func(a *Allocator)observeCollectionsLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	current,p,err:=a.collectionContext(ctx,grant,g)
	if err!=nil{return err}
	defer p.Close()
	raw,err:=boundedRegularBytes(p,"collection-attempt.json",maxCollectionRecord)
	if err!=nil{return err}
	var m collectionAttempt
	if json.Unmarshal(raw,&m)!=nil||!sameCollectionScope(m,current){return ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return ErrStageRecovery}
	expected,err:=expectedCollection(m)
	if err!=nil{return err}
	body,err:=boundedRegularBytes(p,"collection-result.json",maxCollectionRecord)
	if err!=nil{return err}
	actual,err:=inspectCollection(ctx,g,m.ImageID,m.Name)
	if err!=nil||!reflect.DeepEqual(actual,expected){return ErrStageRecovery}
	want,err:=json.Marshal(collectionResult{Version:1,AttemptSHA256:digestBytes(raw),After:actual})
	if err!=nil||!bytes.Equal(body,want){return ErrStageRecovery}
	current,check,err:=a.collectionContext(ctx,grant,g)
	if err!=nil{return err}
	sameRoot:=sameRelationshipRoot(p,check);check.Close()
	if !sameRoot||!sameCollectionScope(m,current){return ErrStageRecovery}
	return nil
}

// Structural validation is deliberately distinct from actual DB proof.
func validateCollectionRecords(p *os.Root,l landingAttempt,metadata metadataResult,hasResult bool)error{
	if len(metadata.Rows)!=1||l.RestoreImageID!=0{return ErrStageRecovery}
	mraw,err:=json.Marshal(metadata)
	if err!=nil{return ErrStageRecovery}
	rel,err:=boundedRegularBytes(p,"relationship-result.json",maxRelationshipRecord)
	if err!=nil{return err}
	raw,err:=boundedRegularBytes(p,"collection-attempt.json",maxCollectionRecord)
	if err!=nil{return err}
	var m collectionAttempt
	if json.Unmarshal(raw,&m)!=nil||m.MetadataSHA256!=digestBytes(mraw)||m.RelationshipSHA256!=digestBytes(rel)||
		m.ImageID!=metadata.Rows[0].ImageID||m.NewOnly!=l.NewOnly{return ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return ErrStageRecovery}
	expected,err:=expectedCollection(m)
	if err!=nil{return err}
	if !hasResult{return nil}
	body,err:=boundedRegularBytes(p,"collection-result.json",maxCollectionRecord)
	if err!=nil{return err}
	want,err:=json.Marshal(collectionResult{Version:1,AttemptSHA256:digestBytes(raw),After:expected})
	if err!=nil||!bytes.Equal(body,want){return ErrStageRecovery}
	return nil
}


// A collection witness can be observed after the temporary landing
// path disappears. The approved row, original empty membership, exact
// result and current actual home/memberships must all agree. This does
// not observe files or confer permission to mutate them.
func newCollectionProof(ctx context.Context,g *library.Gallery,p *os.Root,l landingAttempt,metadata metadataResult,row acquisitionplan.ApprovedPolicyRow)(*collectionState,error){
	if !l.NewOnly{return nil,ErrStageRecovery}
	return collectionProof(ctx,g,p,l,metadata,row)
}

func collectionProof(ctx context.Context,g *library.Gallery,p *os.Root,l landingAttempt,metadata metadataResult,row acquisitionplan.ApprovedPolicyRow)(*collectionState,error){
	if l.RestoreImageID!=0||len(metadata.Rows)!=1||row.Provenance==nil||!validCollectionClaim(row.Provenance){return nil,ErrStageRecovery}
	plan,_,err:=readPublicationAttempt(p,l.Allocation)
	if err!=nil||(plan!=nil&&!matchesCollectionClaim(plan.Collection,row.Provenance)){return nil,ErrStageRecovery}
	if row.Provenance.Collection==""{
		for _,name:=range []string{"collection-attempt.json","collection-result.json"}{
			if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return nil,ErrStageRecovery}
		}
		return nil,nil
	}
	if err:=validateCollectionRecords(p,l,metadata,true);err!=nil{return nil,err}
	raw,err:=boundedRegularBytes(p,"collection-attempt.json",maxCollectionRecord)
	var m collectionAttempt
	if err!=nil||json.Unmarshal(raw,&m)!=nil||m.NewOnly!=l.NewOnly||m.Name!=row.Provenance.Collection||m.Order!=row.Provenance.CollectionOrder||m.ImageID!=metadata.Rows[0].ImageID{return nil,ErrStageRecovery}
	if plan!=nil&&!matchesPlannedCollectionAttempt(plan.Collection,m){return nil,ErrStageRecovery}
	expected,err:=expectedCollection(m)
	if err!=nil{return nil,err}
	actual,err:=inspectCollection(ctx,g,m.ImageID,m.Name)
	if err!=nil||!reflect.DeepEqual(expected,actual){return nil,ErrStageRecovery}
	return &actual.State,nil
}

// Bind optional completed collection evidence to a subsequent naming
// attempt. Absence is exact, not a way to bypass an incomplete attempt.
func collectionResultDigest(p *os.Root)(string,error){
	if _,err:=p.Lstat("collection-attempt.json");errors.Is(err,os.ErrNotExist){
		if _,err:=p.Lstat("collection-result.json");!errors.Is(err,os.ErrNotExist){return "",ErrStageRecovery}
		return "",nil
	}else if err!=nil{return "",ErrStageRecovery}
	raw,err:=boundedRegularBytes(p,"collection-result.json",maxCollectionRecord)
	if err!=nil{return "",err}
	return digestBytes(raw),nil
}
