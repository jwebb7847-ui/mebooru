package acquisitionstage

import(
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/db"
	"github.com/monbooru/monbooru/internal/gallery"
	"github.com/monbooru/monbooru/internal/library"
)

// A metadata attempt is not an atomic library import. Existing source and
// tag services commit independently; retain this marker on any failure.
// Only local disposable caller with gallery lifetime and scan exclusion.
type metadataTarget struct{
	PostID string `json:"post_id"`
	ImageID int64 `json:"image_id"`
	PriorRating string `json:"prior_rating"`
}
type metadataAttempt struct{
	Version int `json:"version"`
	Landing landingAttempt `json:"landing"`
	PolicySHA256 string `json:"policy_sha256"`
	Targets []metadataTarget `json:"targets"`
}
type metadataObservation struct{
	PostID string `json:"post_id"`
	ImageID int64 `json:"image_id"`
	Rating string `json:"rating"`
	Rejected []string `json:"rejected"`
	TagIDs []int64 `json:"tag_ids"`
}
func canonicalRating(s string)bool{
	switch s{case "","general","sensitive","questionable","explicit":return true}
	return false
}
func readMetadataAttempt(p *os.Root,landing landingAttempt,e SealedEvidence)(metadataAttempt,error){
	var zero metadataAttempt
	body,err:=boundedRegularBytes(p,"metadata-attempt.json",maxSealRecordBytes)
	if err!=nil{return zero,err}
	var m metadataAttempt
	if json.Unmarshal(body,&m)!=nil||m.Version!=1||m.Landing!=landing||!validDigest(m.PolicySHA256)||
		m.Targets==nil||len(m.Targets)!=len(e.Files){return zero,ErrStageRecovery}
	seen:=map[int64]bool{}
	for i,t:=range m.Targets{
		if t.PostID!=e.Files[i].ID||t.ImageID<=0||seen[t.ImageID]||!canonicalRating(t.PriorRating){return zero,ErrStageRecovery}
		seen[t.ImageID]=true
	}
	raw,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,body){return zero,ErrStageRecovery}
	return m,nil
}

func readImageRating(ctx context.Context,g *library.Gallery,id int64)(string,error){
	rows,err:=g.DB.Read.QueryContext(ctx,`SELECT t.name FROM image_tags it JOIN tags t ON t.id=it.tag_id
		JOIN tag_categories c ON c.id=t.category_id WHERE it.image_id=? AND c.name='rating'`,id)
	if err!=nil{return "",ErrStageRecovery}
	defer rows.Close()
	var rating string
	for rows.Next(){
		var r string
		if rows.Scan(&r)!=nil||r==""||!canonicalRating(r)||rating!=""{return "",ErrStageRecovery}
		rating=r
	}
	if rows.Err()!=nil{return "",ErrStageRecovery}
	return rating,nil
}

func validMappedRow(r acquisitionplan.ApprovedPolicyRow)bool{
	// First candidate has no fallback URL, unknown-category warning,
	// user extras, or optional source fields. No inferred safe rating.
	u,err:=url.Parse(r.PostURL)
	return err==nil&&u.Scheme=="https"&&u.Hostname()!=""&&u.User==nil&&u.Fragment==""&&
		r.SourceLabel!=""&&strings.TrimSpace(r.SourceLabel)==r.SourceLabel&&
		len(r.UnknownTargets)==0&&canonicalRating(r.Rating)
}
func selectMappedRows(snapshot acquisitionplan.ApprovedPolicySnapshot,e SealedEvidence)([]acquisitionplan.ApprovedPolicyRow,error){
	byID:=map[string]acquisitionplan.ApprovedPolicyRow{}
	for _,r:=range snapshot.Rows{byID[r.PostID]=r}
	out:=make([]acquisitionplan.ApprovedPolicyRow,0,len(e.Files))
	for _,f:=range e.Files{
		r,ok:=byID[f.ID]
		if !ok||!validMappedRow(r){return nil,ErrStageRefused}
		out=append(out,r)
	}
	return out,nil
}

// verifyMappedRow observes actual source membership and tag attribution.
// Source merge intentionally preserves an existing rating (even a
// different source rating), and rejected names remain rejected. Do not
// turn a warning, absent source, or missing attribution into success.
func verifyMappedRow(ctx context.Context,g *library.Gallery,t metadataTarget,r acquisitionplan.ApprovedPolicyRow,rejected []string)(metadataObservation,error){
	var zero metadataObservation
	wantRating:=t.PriorRating
	if wantRating==""{wantRating=r.Rating}
	got,err:=readImageRating(ctx,g,t.ImageID)
	if err!=nil||got!=wantRating{return zero,ErrStageRecovery}
	var ids []int64
	for _,name:=range r.Tags{
		if ctx.Err()!=nil{return zero,ErrStageRefused}
		cat,bare,err:=gallery.ResolveCategoryTag(g.DB,name)
		if err!=nil{return zero,ErrStageRecovery}
		tag,err:=g.TagSvc.FindTag(bare,cat)
		if err!=nil||tag==nil{return zero,ErrStageRecovery}
		if slices.Contains(rejected,name){
			if !tag.Rejected{return zero,ErrStageRecovery}
			continue
		}
		if tag.Rejected{return zero,ErrStageRecovery}
		// SyncSourceTagsWithheld does not claim an already-rated image.
		if strings.HasPrefix(name,"rating:")&&t.PriorRating!=""{continue}
		if !slices.Contains(ids,tag.ID){ids=append(ids,tag.ID)}
	}
	slices.Sort(ids)
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return zero,ErrStageRecovery}
	defer tx.Rollback()
	var actualURL string
	if err:=tx.QueryRowContext(ctx,`SELECT url FROM image_sources WHERE image_id=? AND site=? AND post_id=?`,t.ImageID,r.SourceLabel,t.PostID).Scan(&actualURL);err!=nil||actualURL!=r.PostURL{return zero,ErrStageRecovery}
	for _,id:=range ids{
		var n int
		if err:=tx.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_tags it JOIN image_tag_sources ts
			ON ts.image_id=it.image_id AND ts.tag_id=it.tag_id
			WHERE it.image_id=? AND it.tag_id=? AND ts.source=?`,t.ImageID,id,r.SourceLabel).Scan(&n);err!=nil||n!=1{return zero,ErrStageRecovery}
	}
	if err:=tx.Commit();err!=nil||ctx.Err()!=nil{return zero,ErrStageRecovery}
	return metadataObservation{PostID:t.PostID,ImageID:t.ImageID,Rating:got,Rejected:append([]string{},rejected...),TagIDs:append([]int64{},ids...)},nil
}

// applyMappedMetadata is deliberately unexported and disconnected.
// It requires independently bound policy, actual placed/landed bytes,
// and an existing landing-attempt marker. Before the first metadata
// write, persist the exact target IDs and preexisting rating. No retries
// after any attempt, including if a service commits just its first step.
// Initial candidate refuses multiple posts targeting the same image;
// production must reconcile same-image windows explicitly.
func(a *Allocator)applyMappedMetadata(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)([]metadataObservation,error){
	a.mu.Lock();defer a.mu.Unlock()
	if g==nil||g.WatcherRunning(){return nil,ErrStageRefused}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return nil,err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return nil,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if _,err:=p.Lstat("metadata-attempt.json");!errors.Is(err,os.ErrNotExist){return nil,ErrStageRecovery}
	landing,err:=readLandingAttempt(p,r)
	if err!=nil{return nil,err}
	current,err:=observedDB(r,g)
	if err!=nil||current!=landing{return nil,ErrStageRecovery}
	observed,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||len(observed)!=len(e.Files){return nil,ErrStageRecovery}
	rows,err:=selectMappedRows(snapshot,e)
	if err!=nil{return nil,err}
	_,hash,err:=acquisitionplan.EncodeApprovedPolicy(snapshot.Transfer,snapshot.Rows)
	if err!=nil{return nil,ErrStageRecovery}
	m:=metadataAttempt{Version:1,Landing:landing,PolicySHA256:hash,Targets:make([]metadataTarget,0,len(rows))}
	seen:=map[int64]bool{}
	for i,v:=range observed{
		if v.State!="observed_path"||v.ImageID<=0||v.PostID!=rows[i].PostID||seen[v.ImageID]{return nil,ErrStageRecovery}
		seen[v.ImageID]=true
		rating,err:=readImageRating(ctx,g,v.ImageID)
		if err!=nil{return nil,err}
		m.Targets=append(m.Targets,metadataTarget{PostID:v.PostID,ImageID:v.ImageID,PriorRating:rating})
	}
	stillCurrent:=func()bool{
		s,err:=a.scope(ctx,grant.Request)
		if err!=nil||!matches(r,s)||g.WatcherRunning(){return false}
		c,err:=observedDB(r,g)
		return err==nil&&c==landing
	}
	if !stillCurrent(){return nil,ErrStageRefused}
	body,err:=json.Marshal(m)
	if err!=nil||len(body)>maxSealRecordBytes{return nil,ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"metadata-attempt.json",body);err!=nil{return nil,err}
	syncDirectory(p)
	out:=make([]metadataObservation,0,len(rows))
	for i,row:=range rows{
		if !stillCurrent(){return nil,ErrStageRefused}
		var sum gallery.MergeSummary
		var warnings []string
		err:=g.Edit(func(d *db.DB)error{
			var err error
			sum,warnings,err=gallery.MergeSource(d,g.TagSvc,m.Targets[i].ImageID,row.SourceLabel,row.PostID,row.PostURL,"","",gallery.PostFile{},row.Tags,
				gallery.Withheld{Categories:row.WithheldCategories,Tags:row.WithheldTags})
			return err
		})
		if err!=nil||len(warnings)!=0||!sum.SourceAdded||!stillCurrent(){return nil,ErrStageRecovery}
		v,err:=verifyMappedRow(ctx,g,m.Targets[i],row,sum.RejectedTags)
		if err!=nil{return nil,err}
		out=append(out,v)
	}
	again,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||!slices.Equal(observed,again)||!stillCurrent(){return nil,ErrStageRecovery}
	// Results are deliberately NONTERMINAL. Optional provenance, naming,
	// duplicate-drop, user extras and durable outcome remain unresolved.
	return out,nil
}
