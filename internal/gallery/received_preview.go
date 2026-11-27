package gallery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monbooru/monbooru/internal/db"
)

// ReceivedNamingRequest is owner-side planning input, not a wire DTO
// or authority to write files. The managed caller must independently
// bind the actual job settings and verified received name.
type ReceivedNamingRequest struct {
	Gallery string
	ReceivedName string
	RequestFolder string
	DefaultFolder string
	DefaultName string
	Source string
	PostID string
}

// ReceivedNamingPreview is a point-in-time, read-only description of
// the legacy two-step naming policy. Absolute paths are local only.
// It is not a reservation or evidence that a rename is safe to perform.
type ReceivedNamingPreview struct {
	InitialPath string
	FinalPath string
	InitialSuffixed bool
	FinalSuffixed bool
}

const maxPreviewCandidates = 1024

// PreviewReceivedNaming uses an actual landed row's measured facts,
// while replacing the diagnostic staging filename/folder with the
// name/folder the ordinary upload would have seen. It never creates
// a directory, file, path row or MD5 backfill. The first candidate
// refuses missing local MD5 evidence and unqualified case-only moves;
// legacy naming itself is unchanged.
func PreviewReceivedNaming(ctx context.Context, database *db.DB, b *Boundary, id int64, req ReceivedNamingRequest) (ReceivedNamingPreview,error) {
	var zero ReceivedNamingPreview
	if b==nil || database==nil || id<=0 || req.Gallery=="" || ctx.Err()!=nil {
		return zero,errors.New("invalid received naming scope")
	}
	for _,s:=range []string{req.Gallery,req.ReceivedName,req.RequestFolder,req.DefaultFolder,req.DefaultName,req.Source,req.PostID}{
		if len(s)>4096 || strings.IndexByte(s,0)>=0 { return zero,errors.New("invalid received naming input") }
	}
	if req.ReceivedName=="" || strings.ContainsAny(req.ReceivedName,"/\\") {
		return zero,errors.New("received name must be one filename")
	}
	received:=SanitizeReceivedName(req.ReceivedName)
	if received=="" || received=="." || received!=filepath.Base(received) || !filepath.IsLocal(received) || len(received)>255 {
		return zero,errors.New("invalid received filename")
	}
	// The legacy config parser logs invalid settings and can fall back.
	// A managed preview needs a determinate valid policy before planning.
	if _,err:=ParseNameTemplate(req.DefaultName,ScopeUploadName);err!=nil{return zero,err}
	if req.RequestFolder=="" {
		if _,err:=ParseNameTemplate(req.DefaultFolder,ScopeUploadFolder);err!=nil{return zero,err}
	}
	writeDir,n:=ReceivedNaming(req.Gallery,req.RequestFolder,req.DefaultFolder,req.DefaultName)
	dir,err:=b.ResolveSubdir(writeDir)
	if err!=nil{return zero,err}
	if err:=b.Check(filepath.Join(dir,received));err!=nil{return zero,err}
	initial,numbered,err:=previewAvailablePath(ctx,database,b,dir,received,"",uploadSuffix)
	if err!=nil{return zero,err}
	facts,err:=readNameFacts(ctx,database,req.Gallery,id)
	if err!=nil{return zero,err}
	if filepath.Ext(facts.Base)=="" || !strings.EqualFold(filepath.Ext(facts.Base),filepath.Ext(received)) {
		return zero,errors.New("received extension disagrees with landed row")
	}
	if facts.MD5=="" && readsMD5([]*NameTemplate{n.Folder,n.Name}) {
		return zero,errors.New("local MD5 evidence required before naming preview")
	}
	facts.Base=filepath.Base(initial)
	ext:=filepath.Ext(facts.Base)
	facts.Name=strings.TrimSuffix(facts.Base,ext)
	facts.Ext=strings.ToLower(strings.TrimPrefix(ext,"."))
	facts.Folder=FolderPath(b.Root(),initial)
	folder,name,err:=n.renderArrival(facts,req.Source,req.PostID)
	if err!=nil{return zero,err}
	base,err:=placementBase(name)
	if err!=nil{return zero,err}
	p,err:=planPlacement(b,initial,facts.Folder,folder,name,base)
	if err!=nil{return zero,err}
	out:=ReceivedNamingPreview{InitialPath:initial,FinalPath:initial,InitialSuffixed:numbered}
	if !p.moved && !p.renamed { return out,nil }
	final,numbered,err:=previewAvailablePath(ctx,database,b,p.destDir,p.base,initial,placementSuffix(p.renamed))
	if err!=nil{return zero,err}
	out.FinalPath=final
	out.FinalSuffixed=numbered
	return out,nil
}

// Preserve the two legacy suffix functions, but do not treat every
// stat error as absence. A stale path row at a free filename refuses;
// the managed path must not silently adopt any pre-existing alias.
func previewAvailablePath(ctx context.Context,database *db.DB,b *Boundary,dir,base,hypothetical string,suffix func(string,string,int)string)(string,bool,error){
	ext:=filepath.Ext(base)
	stem:=strings.TrimSuffix(base,ext)
	for i:=0;i<maxPreviewCandidates;i++{
		if err:=ctx.Err();err!=nil{return "",false,err}
		name:=base
		if i>0{name=suffix(stem,ext,i)}
		if len(name)>255||!filepath.IsLocal(name)||filepath.Base(name)!=name{return "",false,errors.New("invalid planned name")}
		path:=filepath.Join(dir,name)
		if hypothetical!=""&&strings.EqualFold(path,hypothetical){
			if path!=hypothetical{return "",false,errors.New("case-only naming requires filesystem qualification")}
			continue
		}
		_,err:=os.Lstat(path)
		if err==nil{continue}
		if !errors.Is(err,os.ErrNotExist){return "",false,fmt.Errorf("check planned path: %w",err)}
		if err:=b.Check(path);err!=nil{return "",false,err}
		var count int
		if err:=database.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM image_paths WHERE path=?`,path).Scan(&count);err!=nil{return "",false,err}
		if count!=0{return "",false,errors.New("planned path already belongs to a library row")}
		return path,i>0,nil
	}
	return "",false,errors.New("naming collision limit exceeded")
}
