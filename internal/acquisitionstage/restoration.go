package acquisitionstage

import(
	"context"
	"errors"
	"os"
	"path/filepath"
	"io"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// Known, previously measured single PNG whose prior top-level
// canonical is genuinely absent and whose only path row is that
// canonical. No placeholder, external path, relation or collection.
// Preserve the existing image identity through the inherited ingest.
func captureRestoreTarget(ctx context.Context,g *library.Gallery,r record,e SealedEvidence,snapshot acquisitionplan.ApprovedPolicySnapshot,dst *os.Root,l *landingAttempt)error{
	rows,err:=newOnlyRows(e,snapshot)
	if err!=nil||len(rows)!=1{return ErrStageRefused}
	f:=e.Files[0]
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return ErrStageRecovery}
	defer tx.Rollback()
	var id,size int64
	var path string
	var missing int
	if err:=tx.QueryRowContext(ctx,`SELECT id,canonical_path,file_size,is_missing FROM images WHERE sha256=?`,f.SHA256).Scan(&id,&path,&size,&missing);err!=nil||
		id<=0||size!=f.Size||missing<0||missing>1||filepath.Dir(path)!=r.GalleryRoot||path==filepath.Join(r.GalleryRoot,finalName(r.Allocation,f)){return ErrStageRefused}
	name:=filepath.Base(path)
	if !filepath.IsLocal(name)||name=="."{return ErrStageRefused}
	if _,err:=dst.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRefused}
	for _,check:=range []struct{q string;args []any;want int}{
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=?`,[]any{id},1},
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND path=? AND is_canonical=1`,[]any{id,path},1},
		{`SELECT COUNT(*) FROM image_sources WHERE parent_url=?`,[]any{rows[0].PostURL},0},
		{`SELECT COUNT(*) FROM image_sources WHERE (site=? AND post_id=?) AND image_id<>?`,[]any{rows[0].SourceLabel,rows[0].PostID,id},0},
	}{
		var n int
		if err:=tx.QueryRowContext(ctx,check.q,check.args...).Scan(&n);err!=nil||n!=check.want{return ErrStageRefused}
	}
	if err:=tx.Commit();err!=nil{return ErrStageRecovery}
	rating,err:=readImageRating(ctx,g,id)
	if err!=nil{return err}
	l.RestoreImageID=id;l.RestorePath=name;l.RestoreRating=rating;l.RestoreMarkedMissing=missing==1
	return nil
}

func verifyRestoredLanding(ctx context.Context,g *library.Gallery,r record,e SealedEvidence,l landingAttempt,dst *os.Root,row landingObservation)error{
	if len(e.Files)!=1||l.RestoreImageID<=0||row.ImageID!=l.RestoreImageID||!row.Canonical||row.State!="observed_path"{return ErrStageRecovery}
	if _,err:=dst.Lstat(l.RestorePath);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	// Legacy missing-row restoration demotes the old path; promotion
	// of an unmarked missing canonical removes its old row. Preserve
	// that distinction rather than rewriting legacy semantics.
	wantOld,wantTotal:=0,1
	if l.RestoreMarkedMissing{wantOld,wantTotal=1,2}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return ErrStageRecovery}
	defer tx.Rollback()
	for _,check:=range []struct{q string;args []any;want int}{
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=?`,[]any{row.ImageID},wantTotal},
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND path=? AND is_canonical=0`,[]any{row.ImageID,filepath.Join(r.GalleryRoot,l.RestorePath)},wantOld},
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND is_canonical=1`,[]any{row.ImageID},1},
	}{
		var n int
		if err:=tx.QueryRowContext(ctx,check.q,check.args...).Scan(&n);err!=nil||n!=check.want{return ErrStageRecovery}
	}
	if err:=tx.Commit();err!=nil{return ErrStageRecovery}
	if l.RestoreRating!=""{
		rating,err:=readImageRating(ctx,g,row.ImageID)
		if err!=nil||rating!=l.RestoreRating{return ErrStageRecovery}
	}
	return nil
}

// This is not a terminal receipt or production activation. Attempt
// precedes actual legacy LandInPlace, no replay after uncertain result.
func(a *Allocator)landRestoredPrepared(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)error{
	a.mu.Lock();defer a.mu.Unlock()
	if err:=a.beginLandingModeLocked(ctx,grant,g,false,true);err!=nil{return err}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	l,err:=readLandingAttempt(p,r)
	if err!=nil||l.RestoreImageID<=0||len(e.Files)!=1{return ErrStageRecovery}
	snapshot,err:=a.boundPolicyLocked(ctx,grant)
	if err!=nil{return err}
	current,err:=observedDB(r,g)
	if err!=nil{return err}
	if err:=captureRestoreTarget(ctx,g,r,e,snapshot,dst,&current);err!=nil||current!=l{return ErrStageRecovery}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return ErrStageRefused}
	f:=e.Files[0]
	name:=finalName(grant,f)
	if err:=readCheckedLinks(ctx,dst,name,f.Size,f.SHA256,io.Discard,2);err!=nil{return err}
	if _,err:=dst.Lstat(l.RestorePath);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	res,err:=g.LandInPlace(ctx,filepath.Join(r.GalleryRoot,name),library.LandSpec{Origin:newReceiptOrigin,MaxBytes:f.Size})
	if err!=nil||res.Image==nil||res.Duplicate||res.Dropped||res.Image.ID!=l.RestoreImageID{return ErrStageRecovery}
	observed,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||len(observed)!=1||!observed[0].Canonical||observed[0].ImageID!=l.RestoreImageID{return ErrStageRecovery}
	rating,err:=readImageRating(ctx,g,l.RestoreImageID)
	if err!=nil||rating!=l.RestoreRating{return ErrStageRecovery}
	return nil
}
