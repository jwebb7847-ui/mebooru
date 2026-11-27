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
	"github.com/jwebb7847-ui/mebooru/shared/contract"
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
	DerivedTags []int64 `json:"derived_tags,omitempty"`
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
		if !landing.NewOnly&&len(t.DerivedTags)!=0{return zero,ErrStageRecovery}
		var prior int64
		for _,id:=range t.DerivedTags{
			if id<=prior{return zero,ErrStageRecovery}
			prior=id
		}
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
// Apply the existing import normalization to exact source claims.
// Collection and relationship application are distinct later stages;
// do not silently accept collection requests in this diagnostic path.
func normalizedMappedSource(r acquisitionplan.ApprovedPolicyRow)(gallery.SourceInput,string,error){
	var zero gallery.SourceInput
	f:=contract.PostFields{Source:r.SourceLabel,PostID:r.PostID,URL:r.PostURL,Tags:r.Tags,
		WithheldCategories:r.WithheldCategories,WithheldTags:r.WithheldTags}
	var md5 string
	if p:=r.Provenance;p!=nil{
		if p.Collection!=""||p.CollectionOrder!=0{return zero,"",ErrStageRefused}
		md5=strings.TrimSpace(p.SourceMD5)
		f.ParentURL=p.ParentURL;f.Original=p.Original;f.Commentary=p.Commentary;f.CommentaryDText=p.CommentaryDText
		f.CommentaryTranslated=p.CommentaryTranslated;f.CommentaryTranslatedDText=p.CommentaryTranslatedDText
		f.Notes=p.Notes;f.PostWidth=p.PostWidth;f.PostHeight=p.PostHeight;f.PostSize=p.PostSize;f.PostExt=p.PostExt
	}
	input,err:=gallery.NormalizeSourceInput(f)
	if err!=nil||input.Source!=r.SourceLabel||input.PostID!=r.PostID||input.URL!=r.PostURL{return zero,"",ErrStageRefused}
	return input,md5,nil
}
func selectMappedRows(snapshot acquisitionplan.ApprovedPolicySnapshot,e SealedEvidence)([]acquisitionplan.ApprovedPolicyRow,error){
	byID:=map[string]acquisitionplan.ApprovedPolicyRow{}
	for _,r:=range snapshot.Rows{byID[r.PostID]=r}
	out:=make([]acquisitionplan.ApprovedPolicyRow,0,len(e.Files))
	for _,f:=range e.Files{
		r,ok:=byID[f.ID]
		if !ok||!validMappedRow(r){return nil,ErrStageRefused}
		if _,_,err:=normalizedMappedSource(r);err!=nil{return nil,err}
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
	seenRejected:=map[string]bool{}
	for _,name:=range rejected{
		if !slices.Contains(r.Tags,name)||seenRejected[name]{return zero,ErrStageRecovery}
		seenRejected[name]=true
	}
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
	input,md5,err:=normalizedMappedSource(r)
	if err!=nil{return zero,err}
	if err:=verifySourceClaims(ctx,tx,t.ImageID,input,md5);err!=nil{return zero,err}
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
	current.NewOnly=landing.NewOnly
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
		target:=metadataTarget{PostID:v.PostID,ImageID:v.ImageID,PriorRating:rating}
		if landing.NewOnly{
			if rating!=""{return nil,ErrStageRecovery}
			target.DerivedTags,err=readNewDerivedBaseline(ctx,g,v.ImageID)
			if err!=nil{return nil,err}
		}
		m.Targets=append(m.Targets,target)
	}
	stillCurrent:=func()bool{
		s,err:=a.scope(ctx,grant.Request)
		if err!=nil||!matches(r,s)||g.WatcherRunning(){return false}
		c,err:=observedDB(r,g)
		c.NewOnly=landing.NewOnly
		return err==nil&&c==landing
	}
	if !stillCurrent(){return nil,ErrStageRefused}
	// Bound the worst-case observation record before any mutation.
	// Rejection strings can echo every projected tag; IDs are at most
	// 20 decimal digits plus delimiter. JSON escaping can expand names.
	estimate:=4096+len(rows)*1024
	for _,row:=range rows{
		for _,name:=range row.Tags{
			raw,err:=json.Marshal(name)
			if err!=nil{return nil,ErrStageRefused}
			estimate+=len(raw)+24
			if estimate>maxMetadataResultBytes{return nil,ErrStageLimit}
		}
	}
	body,err:=json.Marshal(m)
	if err!=nil||len(body)>maxSealRecordBytes{return nil,ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"metadata-attempt.json",body);err!=nil{return nil,err}
	syncDirectory(p)
	out:=make([]metadataObservation,0,len(rows))
	for i,row:=range rows{
		if !stillCurrent(){return nil,ErrStageRefused}
		var sum gallery.MergeSummary
		var warnings []string
		input,md5,err:=normalizedMappedSource(row)
		if err!=nil{return nil,err}
		err=g.Edit(func(d *db.DB)error{
			var err error
			sum,warnings,err=gallery.MergeSource(d,g.TagSvc,m.Targets[i].ImageID,input.Source,input.PostID,input.URL,md5,input.ParentURL,input.PostFile,row.Tags,input.Withheld)
			if err!=nil||len(warnings)!=0{return err}
			// Same existing source-provenance services; empty claims
			// preserve prior values and manual annotations stay intact.
			_,err=gallery.ApplySourceProvenance(d,m.Targets[i].ImageID,input.Source,input.PostID,input.Commentary,input.Translated,input.Original,input.Notes)
			return err
		})
		if err!=nil||len(warnings)!=0||!sum.SourceAdded||!stillCurrent(){return nil,ErrStageRecovery}
		v,err:=verifyMappedRow(ctx,g,m.Targets[i],row,sum.RejectedTags)
		if err!=nil{return nil,err}
		out=append(out,v)
	}
	again,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||!slices.Equal(observed,again)||!stillCurrent(){return nil,ErrStageRecovery}
	// Results are deliberately NONTERMINAL. Parent relations, naming,
	// duplicate-drop and user extras remain unresolved. Recheck all
	// rows after the final service write, then persist exact observation.
	for i,row:=range rows{
		v,err:=verifyMappedRow(ctx,g,m.Targets[i],row,out[i].Rejected)
		if err!=nil||!sameMetadataObservation(v,out[i]){return nil,ErrStageRecovery}
	}
	if !stillCurrent(){return nil,ErrStageRefused}
	attemptHash,err:=metadataAttemptDigest(m)
	if err!=nil{return nil,err}
	result:=metadataResult{Version:1,AttemptSHA256:attemptHash,Rows:out}
	raw,err:=json.Marshal(result)
	if err!=nil||len(raw)>maxMetadataResultBytes{return nil,ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"metadata-result.json",raw);err!=nil{return nil,err}
	syncDirectory(p)
	if !stillCurrent(){return nil,ErrStageRefused}
	return a.observeMappedMetadataLocked(ctx,grant,g)
}
