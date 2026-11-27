package acquisitionstage

import(
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// This diagnostic boundary intentionally does not call the legacy
// ingestion routine itself. The actual createImage flow performs many
// separate transactions for bytes, source/rating/tags/provenance; an
// image row alone is not a completed import or a durable receipt.
type landingAttempt struct{
	Version int `json:"version"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	GalleryIdentity string `json:"gallery_identity"`
	DBPath string `json:"db_path"`
	DBFileIdentity string `json:"db_file_identity"`
}
type landingObservation struct{
	PostID string
	ImageID int64
	State string // absent or observed_path; never committed
	Canonical bool
}

func readLandingAttempt(p *os.Root,r record)(landingAttempt,error){
	var zero landingAttempt
	body,err:=boundedRegularBytes(p,"landing-attempt.json",maxRecordBytes)
	if err!=nil{return zero,err}
	var attempt landingAttempt
	if json.Unmarshal(body,&attempt)!=nil||attempt.Version!=1||attempt.Allocation!=r.Allocation||
		attempt.GalleryIdentity!=r.GalleryIdentity||!filepath.IsAbs(attempt.DBPath)||
		filepath.Clean(attempt.DBPath)!=attempt.DBPath||attempt.DBFileIdentity==""||len(attempt.DBFileIdentity)>128{return zero,ErrStageRecovery}
	canonical,err:=json.Marshal(attempt)
	if err!=nil||!bytes.Equal(body,canonical){return zero,ErrStageRecovery}
	return attempt,nil
}
func observedDB(r record,g *library.Gallery)(landingAttempt,error){
	var zero landingAttempt
	if g==nil||g.Closed()||g.Degraded||g.DB==nil||g.DB.Read==nil||
		g.Name!=r.Allocation.Request.Transfer.Metadata.Gallery||g.GalleryPath!=r.GalleryRoot||
		!filepath.IsAbs(g.DBPath)||filepath.Clean(g.DBPath)!=g.DBPath{return zero,ErrStageRefused}
	resolved,err:=filepath.EvalSymlinks(g.DBPath)
	if err!=nil||resolved!=g.DBPath{return zero,ErrStageRefused}
	info,err:=os.Lstat(g.DBPath)
	if err!=nil||!info.Mode().IsRegular(){return zero,ErrStageRefused}
	f,err:=os.Open(g.DBPath)
	if err!=nil{return zero,ErrStageRefused}
	defer f.Close()
	actual,err:=f.Stat()
	if err!=nil||!os.SameFile(info,actual)||!singleLink(f,actual){return zero,ErrStageRefused}
	id,ok:=diskFileIdentity(f,actual)
	if !ok{return zero,ErrStageRefused}
	return landingAttempt{Version:1,Allocation:r.Allocation,GalleryIdentity:r.GalleryIdentity,DBPath:g.DBPath,DBFileIdentity:id},nil
}

// The gallery lifetime, scan exclusion and stable DB binding must be
// supplied by the trusted library owner, not inferred from this path.
// This candidate only operates in disposable galleries without a watcher.
// Neither a closed watcher nor the allocator lock proves global exclusion.
func(a *Allocator)beginLanding(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	a.mu.Lock();defer a.mu.Unlock()
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	if g==nil||g.WatcherRunning(){return ErrStageRefused}
	attempt,err:=observedDB(r,g)
	if err!=nil{return err}
	if _,err:=p.Lstat("landing-attempt.json");!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	if _,err:=boundedRegularBytes(p,"attempt",64);err!=nil{return ErrStageRecovery}
	places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
	if err!=nil{return err}
	for _,v:=range places{if v.State!="placed"{return ErrStageRecovery}}
	// A pre-existing path row cannot be attributed to this operation.
	observations,err:=readLandingRows(ctx,g,r,e)
	if err!=nil{return err}
	for _,v:=range observations{if v.State!="absent"{return ErrStageRecovery}}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s){return ErrStageRefused}
	body,err:=json.Marshal(attempt)
	if err!=nil||len(body)>maxRecordBytes{return ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"landing-attempt.json",body);err!=nil{return err}
	syncDirectory(p)
	// Once this marker exists a crash is uncertain. Never use absence
	// of a path row to authorize another ingestion attempt.
	current,err:=observedDB(r,g)
	if err!=nil||current!=attempt||ctx.Err()!=nil{return ErrStageRecovery}
	return nil
}

// A read-only snapshot of the existing real gallery schema. Does not
// infer source provenance, duplicate/drop outcome, tags or final commit.
func readLandingRows(ctx context.Context,g *library.Gallery,r record,e SealedEvidence)([]landingObservation,error){
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return nil,ErrStageRecovery}
	defer tx.Rollback()
	out:=make([]landingObservation,0,len(e.Files))
	for _,f:=range e.Files{
		var id,size int64
		var sha,canonicalPath string
		var missing,canonical int
		path:=filepath.Join(r.GalleryRoot,finalName(r.Allocation,f))
		err:=tx.QueryRowContext(ctx,`SELECT i.id,i.sha256,i.file_size,i.is_missing,ip.is_canonical,i.canonical_path
			FROM image_paths ip JOIN images i ON i.id=ip.image_id WHERE ip.path=?`,path).
			Scan(&id,&sha,&size,&missing,&canonical,&canonicalPath)
		if errors.Is(err,sql.ErrNoRows){
			// Check the path table itself rather than equating an orphan
			// path row (e.g. corrupt DB) with a clean absence.
			var count int
			if err:=tx.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_paths WHERE path=?`,path).Scan(&count);err!=nil||count!=0{return nil,ErrStageRecovery}
			out=append(out,landingObservation{PostID:f.ID,State:"absent"})
			continue
		}
		if err!=nil||id<=0||sha!=f.SHA256||size!=f.Size||missing!=0||
			canonical!=0&&canonical!=1||canonicalPath==""||
			(canonical==1)!=(canonicalPath==path){return nil,ErrStageRecovery}
		out=append(out,landingObservation{PostID:f.ID,ImageID:id,State:"observed_path",Canonical:canonical==1})
	}
	if err:=tx.Commit();err!=nil||ctx.Err()!=nil{return nil,ErrStageRecovery}
	return out,nil
}
func(a *Allocator)observeLanding(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)([]landingObservation,error){
	a.mu.Lock();defer a.mu.Unlock()
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return nil,err}
	defer p.Close();defer src.Close();defer dst.Close()
	attempt,err:=readLandingAttempt(p,r)
	if err!=nil{return nil,err}
	current,err:=observedDB(r,g)
	if err!=nil||current!=attempt{return nil,ErrStageRecovery}
	places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
	if err!=nil{return nil,err}
	for _,v:=range places{if v.State!="placed"{return nil,ErrStageRecovery}}
	rows,err:=readLandingRows(ctx,g,r,e)
	if err!=nil{return nil,err}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s){return nil,ErrStageRefused}
	current,err=observedDB(r,g)
	if err!=nil||current!=attempt{return nil,ErrStageRecovery}
	if _,err:=inspectPlacements(ctx,grant,e,p,src,dst);err!=nil{return nil,err}
	return rows,nil
}
