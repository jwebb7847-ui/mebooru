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
	// Actual row must still name a present regular in-gallery file.
	// This is a point-in-time prerequisite, not a byte-identity witness.
	canonical,_,err:=loadMoveSource(database,b,id)
	if err!=nil{return zero,err}
	actual,err:=os.Lstat(canonical)
	if err!=nil||!actual.Mode().IsRegular(){return zero,errors.New("landed row has no regular canonical file")}
	facts,err:=readNameFacts(ctx,database,req.Gallery,id)
	if err!=nil{return zero,err}
	if facts.Base!=filepath.Base(canonical)||facts.Size!=actual.Size()||filepath.Ext(facts.Base)=="" || !strings.EqualFold(filepath.Ext(facts.Base),filepath.Ext(received)) {
		return zero,errors.New("received extension disagrees with landed row")
	}
	return previewReceivedWithFacts(ctx,database,b,req,received,facts,false)
}

// PreviewUnlandedReceivedNaming is a read-only first pre-publication candidate.
// Only facts known before ingestion are allowed. ID/time, measured format,
// dimensions, local MD5 and other landed-only facts are deliberately refused;
// a template that would fall back to a future row ID also refuses.
func PreviewUnlandedReceivedNaming(ctx context.Context, database *db.DB, b *Boundary, req ReceivedNamingRequest, sha256 string, size int64) (ReceivedNamingPreview,error) {
	var zero ReceivedNamingPreview
	if b==nil||database==nil||ctx.Err()!=nil||req.Gallery==""||len(sha256)!=64||size<=0{return zero,errors.New("invalid unlanded naming scope")}
	for _,s:=range []string{req.Gallery,req.ReceivedName,req.RequestFolder,req.DefaultFolder,req.DefaultName,req.Source,req.PostID}{
		if len(s)>4096||strings.IndexByte(s,0)>=0{return zero,errors.New("invalid received naming input")}
	}
	if req.ReceivedName==""||strings.ContainsAny(req.ReceivedName,"/\\"){return zero,errors.New("received name must be one filename")}
	received:=SanitizeReceivedName(req.ReceivedName)
	if received==""||received=="."||received!=filepath.Base(received)||!filepath.IsLocal(received)||len(received)>255{return zero,errors.New("invalid received filename")}
	facts:=NameFacts{Gallery:req.Gallery,SHA256:sha256,Size:size}
	return previewReceivedWithFacts(ctx,database,b,req,received,facts,true)
}

func previewReceivedWithFacts(ctx context.Context,database *db.DB,b *Boundary,req ReceivedNamingRequest,received string,facts NameFacts,unlanded bool)(ReceivedNamingPreview,error){
	var zero ReceivedNamingPreview
	// HTTP trims the request folder before choosing defaults. Legacy
	// invalid settings log/fall back; a managed preview refuses them.
	requestFolder:=strings.TrimSpace(req.RequestFolder)
	if _,err:=ParseNameTemplate(req.DefaultName,ScopeUploadName);err!=nil{return zero,err}
	if requestFolder=="" {
		if _,err:=ParseNameTemplate(req.DefaultFolder,ScopeUploadFolder);err!=nil{return zero,err}
	}
	writeDir,n:=ReceivedNaming(req.Gallery,requestFolder,req.DefaultFolder,req.DefaultName)
	dir,err:=b.ResolveSubdir(writeDir)
	if err!=nil{return zero,err}
	if err:=b.Check(filepath.Join(dir,received));err!=nil{return zero,err}
	initial,numbered,err:=previewAvailablePath(ctx,database,b,dir,received,"",uploadSuffix)
	if err!=nil{return zero,err}
	if unlanded{
		for _,tmpl:=range []*NameTemplate{n.Folder,n.Name}{
			if tmpl==nil{continue}
			for _,part:=range tmpl.parts{
				switch part.tok{
				case tokLiteral,tokName,tokExt,tokHash,tokGallery,tokSize,tokFolder,tokSource,tokPostID:
				default:return zero,errors.New("naming template requires landed facts")
				}
			}
		}
	}
	if facts.MD5==""&&readsMD5([]*NameTemplate{n.Folder,n.Name}){return zero,errors.New("local MD5 evidence required before naming preview")}
	facts.Base=filepath.Base(initial)
	ext:=filepath.Ext(facts.Base)
	facts.Name=strings.TrimSuffix(facts.Base,ext)
	facts.Ext=strings.ToLower(strings.TrimPrefix(ext,"."))
	facts.Folder=FolderPath(b.Root(),initial)
	if unlanded&&n.Name!=nil{
		known:=facts
		known.Source,known.PostID=strings.TrimSpace(req.Source),strings.TrimSpace(req.PostID)
		if n.Name.render(known)==""{return zero,errors.New("naming requires future row ID")}
	}
	folder,name,err:=n.renderArrival(facts,strings.TrimSpace(req.Source),strings.TrimSpace(req.PostID))
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
	if err:=previewDirectory(b,dir);err!=nil{return "",false,err}
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

// Inspect all extant ancestors. ENOTDIR can be reported as not-exist
// by some platforms; do not plan beneath an ordinary file or symlink.
// This still does not reserve a directory or eliminate later races.
func previewDirectory(b *Boundary,dir string)error{
	root:=filepath.Clean(b.Root())
	for at:=filepath.Clean(dir);;at=filepath.Dir(at){
		if !PathInside(root,at){return errors.New("planned directory escapes gallery")}
		info,err:=os.Lstat(at)
		if err==nil{
			if !info.IsDir()||info.Mode()&os.ModeSymlink!=0{return errors.New("planned directory has non-directory ancestor")}
		}else if !errors.Is(err,os.ErrNotExist){return err}
		if pathKey(at)==pathKey(root){
			if err!=nil{return err}
			return nil
		}
		if filepath.Dir(at)==at{return errors.New("planned directory escapes gallery")}
	}
}
