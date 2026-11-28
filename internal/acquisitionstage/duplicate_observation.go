package acquisitionstage

import(
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// A read-only duplicate-disposition witness, not a deletion grant or
// terminal receipt. The existing canonical object must be independently
// verified, not merely have a matching SHA stored in SQLite. This first
// candidate is for a held duplicate with a separate canonical object;
// missing/restored/promoted canonicals are separate cases.
type heldDuplicate struct{
	PostID string
	ImageID int64
	CanonicalPath string
	CanonicalIdentity string
	Size int64
	SHA256 string
}
func duplicateCanonicalRow(ctx context.Context,tx *sql.Tx,root,alias string,id int64,f acquisitionplan.VerifiedTransferFile)(string,error){
	return duplicateCanonicalRowMode(ctx,tx,root,alias,id,f,1)
}

// The guarded prepublication observer requires the managed alias to be absent.
// The historical post-landing witness continues requiring exactly one alias.
func duplicateCanonicalRowMode(ctx context.Context,tx *sql.Tx,root,alias string,id int64,f acquisitionplan.VerifiedTransferFile,aliasCount int)(string,error){
	if aliasCount!=0&&aliasCount!=1{return "",ErrStageRecovery}
	var path,sha string
	var size int64
	var missing int
	if err:=tx.QueryRowContext(ctx,`SELECT canonical_path,sha256,file_size,is_missing FROM images WHERE id=?`,id).Scan(&path,&sha,&size,&missing);err!=nil||
		sha!=f.SHA256||size!=f.Size||missing!=0||path==alias||!filepath.IsAbs(path)||filepath.Clean(path)!=path{return "",ErrStageRecovery}
	rel,err:=filepath.Rel(root,path)
	if err!=nil||rel=="."||!filepath.IsLocal(rel){return "",ErrStageRecovery}
	var count int
	for _,check:=range []struct{q string;args []any;want int}{
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND path=? AND is_canonical=1`,[]any{id,path},1},
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND path=? AND is_canonical=0`,[]any{id,alias},aliasCount},
		{`SELECT COUNT(*) FROM image_paths WHERE image_id=? AND is_canonical=1`,[]any{id},1},
	}{
		if err:=tx.QueryRowContext(ctx,check.q,check.args...).Scan(&count);err!=nil||count!=check.want{return "",ErrStageRecovery}
	}
	return rel,nil
}

// Open each directory component independently, never accepting a
// symlinked canonical parent. Hash the separately owned canonical file,
// require a regular single-link object and recheck its identity. This
// uses the existing rooted checked reader; no hostile same-user race
// guarantee, and no filesystem mutation.
func verifyHeldCanonical(ctx context.Context,root *os.Root,rel string,f acquisitionplan.VerifiedTransferFile)(string,error){
	if !filepath.IsLocal(rel)||filepath.Clean(rel)!=rel||rel=="."{return "",ErrStageRecovery}
	parts:=strings.Split(rel,string(os.PathSeparator))
	parent:=root
	for _,part:=range parts[:len(parts)-1]{
		next,err:=openDir(parent,part)
		if err!=nil{return "",err}
		defer next.Close()
		parent=next
	}
	name:=parts[len(parts)-1]
	before,err:=parent.Lstat(name)
	if err!=nil||!before.Mode().IsRegular(){return "",ErrStageRecovery}
	file,err:=parent.Open(name)
	if err!=nil{return "",ErrStageRecovery}
	defer file.Close()
	actual,err:=file.Stat()
	if err!=nil||!os.SameFile(before,actual)||!singleLink(file,actual){return "",ErrStageRecovery}
	id,ok:=diskFileIdentity(file,actual)
	if !ok{return "",ErrStageRecovery}
	if err:=readChecked(ctx,parent,name,f.Size,f.SHA256,io.Discard);err!=nil{return "",err}
	after,err:=root.Lstat(rel)
	if err!=nil||!after.Mode().IsRegular()||!os.SameFile(before,after)||after.ModTime()!=before.ModTime()||
		after.Size()!=f.Size||!singleLink(file,after)||ctx.Err()!=nil{return "",ErrStageRecovery}
	return id,nil
}
func(a *Allocator)observeHeldDuplicates(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)([]heldDuplicate,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeHeldDuplicatesLocked(ctx,grant,g)
}
func(a *Allocator)observeHeldDuplicatesLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery)([]heldDuplicate,error){
	if g==nil||g.WatcherRunning(){return nil,ErrStageRefused}
	// Existing mapped phase proves policy, metadata, exact destination
	// link and DB identity. Do not drop any copy after a partial merge.
	metadata,err:=a.observeMappedMetadataLocked(ctx,grant,g)
	if err!=nil||len(metadata)==0{return nil,ErrStageRecovery}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return nil,err}
	defer p.Close();defer src.Close();defer dst.Close()
	landing,err:=readLandingAttempt(p,r)
	if err!=nil||landing.NewOnly||landing.RestoreImageID!=0||len(metadata)!=len(e.Files){return nil,ErrStageRecovery}
	landed,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||len(landed)!=len(e.Files){return nil,ErrStageRecovery}
	tx,err:=g.DB.Read.BeginTx(ctx,&sql.TxOptions{ReadOnly:true})
	if err!=nil{return nil,ErrStageRecovery}
	defer tx.Rollback()
	out:=make([]heldDuplicate,0,len(e.Files))
	for i,f:=range e.Files{
		row:=landed[i]
		if row.Canonical||row.State!="observed_path"||row.ImageID!=metadata[i].ImageID||row.PostID!=f.ID{return nil,ErrStageRecovery}
		alias:=filepath.Join(r.GalleryRoot,finalName(grant,f))
		rel,err:=duplicateCanonicalRow(ctx,tx,r.GalleryRoot,alias,row.ImageID,f)
		if err!=nil{return nil,err}
		id,err:=verifyHeldCanonical(ctx,dst,rel,f)
		if err!=nil{return nil,err}
		out=append(out,heldDuplicate{PostID:f.ID,ImageID:row.ImageID,CanonicalPath:rel,CanonicalIdentity:id,Size:f.Size,SHA256:f.SHA256})
	}
	if err:=tx.Commit();err!=nil{return nil,ErrStageRecovery}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,s)||g.WatcherRunning(){return nil,ErrStageRefused}
	current,err:=observedDB(r,g)
	if err!=nil||current!=landing{return nil,ErrStageRecovery}
	again,err:=a.observeLandingLocked(ctx,grant,g)
	if err!=nil||len(again)!=len(landed){return nil,ErrStageRecovery}
	for i,row:=range again{if row!=landed[i]{return nil,ErrStageRecovery}}
	return out,nil
}
