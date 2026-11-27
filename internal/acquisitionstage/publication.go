package acquisitionstage

import(
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Local disposable candidate only. The eventual library caller must
// hold gallery lifetime/exclusive publication and scanner exclusion,
// verify actual custody and current commit authority. No HTTP or IPC
// caller, database mutation, production naming policy or cleanup yet.
type publicationIntent struct{
	Version int `json:"version"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	GalleryIdentity string `json:"gallery_identity"`
	GalleryRoot string `json:"gallery_root"`
	Files []acquisitionplan.VerifiedTransferFile `json:"files"`
}
type placement struct{
	ID string
	Name string
	State string // prepared, absent after attempt, or placed; NOT a receipt
}

func finalName(g workerproto.StageAllocation,f acquisitionplan.VerifiedTransferFile)string{
	// Deterministic, library-generated diagnostic filename only.
	return "mebooru-"+g.TransferID+"-"+f.RelativeName
}
func readPublication(operation *os.Root,r record,e SealedEvidence)(publicationIntent,error){
	var zero publicationIntent
	p,err:=openDir(operation,"publication")
	if err!=nil{return zero,err}
	defer p.Close()
	entries,err:=list(p,16)
	if err!=nil||len(entries)<2||len(entries)>16{return zero,ErrStageRecovery}
	names:=map[string]bool{}
	for _,entry:=range entries{
		name:=entry.Name()
		switch name{
		case "media","intent.json","attempt","landing-attempt.json","metadata-attempt.json","metadata-result.json","relationship-attempt.json","relationship-result.json","collection-attempt.json","collection-result.json","naming-attempt.json","naming-result.json","extras-attempt.json","extras-result.json","composed-receipt.json","new-receipt.json","duplicate-attempt.json":
			names[name]=true
		default:return zero,ErrStageRecovery
		}
	}
	if !names["media"]||!names["intent.json"]||
		(names["landing-attempt.json"]&&!names["attempt"])||
		(names["metadata-attempt.json"]&&!names["landing-attempt.json"])||
		(names["metadata-result.json"]&&!names["metadata-attempt.json"])||
		(names["relationship-attempt.json"]&&!names["metadata-result.json"])||
		(names["relationship-result.json"]&&!names["relationship-attempt.json"])||
		(names["collection-attempt.json"]&&!names["relationship-result.json"])||
		(names["collection-result.json"]&&!names["collection-attempt.json"])||
		(names["naming-attempt.json"]&&!names["relationship-result.json"])||
		(names["naming-result.json"]&&!names["naming-attempt.json"])||
		(names["extras-attempt.json"]&&!names["naming-result.json"])||
		(names["extras-result.json"]&&!names["extras-attempt.json"])||
		(names["composed-receipt.json"]&&!names["extras-result.json"])||
		(names["naming-attempt.json"]&&(names["collection-attempt.json"]||names["new-receipt.json"]||names["duplicate-attempt.json"]))||
		(names["collection-attempt.json"]&&(names["new-receipt.json"]||names["duplicate-attempt.json"]))||
		((names["new-receipt.json"]||names["duplicate-attempt.json"])&&!names["metadata-result.json"])||
		(names["new-receipt.json"]&&names["duplicate-attempt.json"])||
		((names["new-receipt.json"]||names["duplicate-attempt.json"])&&names["relationship-attempt.json"]&&!names["relationship-result.json"]){return zero,ErrStageRecovery}
	raw,err:=boundedRegularBytes(p,"intent.json",maxSealRecordBytes)
	if err!=nil{return zero,err}
	var intent publicationIntent
	if json.Unmarshal(raw,&intent)!=nil||intent.Version!=1||intent.Allocation!=r.Allocation||
		intent.GalleryIdentity!=r.GalleryIdentity||intent.GalleryRoot!=r.GalleryRoot||intent.Files==nil||
		!slices.Equal(intent.Files,e.Files){return zero,ErrStageRecovery}
	canonical,err:=json.Marshal(intent)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	media,err:=openDir(p,"media")
	if err!=nil{return zero,err}
	defer media.Close()
	items,err:=list(media,len(intent.Files))
	if err!=nil||len(items)!=len(intent.Files){return zero,ErrStageRecovery}
	want:=map[string]bool{}
	for _,f:=range intent.Files{want[f.RelativeName]=true}
	for _,item:=range items{
		if !item.Type().IsRegular()||!want[item.Name()]{return zero,ErrStageRecovery}
		delete(want,item.Name())
	}
	if names["attempt"]{
		marker,err:=boundedRegularBytes(p,"attempt",64)
		if err!=nil||string(marker)!=r.Allocation.TransferID{return zero,ErrStageRecovery}
	}
	if names["landing-attempt.json"]{
		landing,err:=readLandingAttempt(p,r)
		if err!=nil{return zero,ErrStageRecovery}
		if names["metadata-attempt.json"]{
			m,err:=readMetadataAttempt(p,landing,e)
			if err!=nil{return zero,ErrStageRecovery}
			if names["metadata-result.json"]{
				result,err:=readMetadataResult(p,m)
				if err!=nil{return zero,ErrStageRecovery}
				if names["relationship-attempt.json"]{
					if err:=validateRelationshipRecords(p,result,names["relationship-result.json"]);err!=nil{return zero,err}
				}
				if names["collection-attempt.json"]{
					if err:=validateCollectionRecords(p,landing,result,names["collection-result.json"]);err!=nil{return zero,err}
				}
				if names["naming-attempt.json"]{
					if err:=validateNamingRecords(p,r,e,landing,result,names["naming-result.json"]);err!=nil{return zero,err}
				}
				if names["extras-attempt.json"]{
					if err:=validateExtrasRecords(p,landing,result,names["extras-result.json"]);err!=nil{return zero,err}
				}
				if names["composed-receipt.json"]{
					if _,err:=readComposedReceipt(p,r,e,landing,m,result);err!=nil{return zero,err}
				}
				if names["new-receipt.json"]{
					if !landing.NewOnly{return zero,ErrStageRecovery}
					if _,err:=readNewReceipt(p,r,e,m,result);err!=nil{return zero,ErrStageRecovery}
				}
				if names["duplicate-attempt.json"]{
					if landing.NewOnly{return zero,ErrStageRecovery}
					if _,err:=readDuplicateAttempt(p,e,result);err!=nil{return zero,ErrStageRecovery}
				}
			}
		}
	}
	return intent,nil
}
func writeOwnerBytes(ctx context.Context,r *os.Root,name string,body []byte)error{
	return writeChecked(ctx,r,name,int64(len(body)),digestBytes(body),func(w io.Writer)error{
		_,err:=io.Copy(w,bytes.NewReader(body));return err
	})
}

// preparePublication records an exact intent only after all private
// candidate bytes are complete and synced. A partial preparation blocks
// automatic replay. The sealed copy remains independent of gallery bytes.
func(a *Allocator)preparePublication(ctx context.Context,g workerproto.StageAllocation)error{
	a.mu.Lock();defer a.mu.Unlock()
	e,err:=a.sealedLocked(ctx,g)
	if err!=nil{return err}
	s,err:=a.scope(ctx,g.Request)
	if err!=nil{return err}
	r,err:=a.load(g.Request.OperationID)
	if err!=nil||!matches(r,s)||r.Allocation!=g{return ErrStageRefused}
	op,err:=openDir(a.root,g.Request.OperationID)
	if err!=nil{return err};defer op.Close()
	if _,err:=op.Lstat("publication");!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	var total uint64
	for _,f:=range e.Files{total+=uint64(f.Size)}
	free,err:=a.freeBytes(a.path)
	if err!=nil||free<total+maxSealRecordBytes{return ErrStageLimit}
	sealed,err:=openDir(op,"sealed")
	if err!=nil{return err};defer sealed.Close()
	src,err:=openDir(sealed,"media")
	if err!=nil{return err};defer src.Close()
	if err:=op.Mkdir("publication",0700);err!=nil{return ErrStageRecovery}
	p,err:=openDir(op,"publication")
	if err!=nil{return err};defer p.Close()
	if err:=p.Mkdir("media",0700);err!=nil{return ErrStageRecovery}
	dst,err:=openDir(p,"media")
	if err!=nil{return err};defer dst.Close()
	for _,f:=range e.Files{
		if err:=writeChecked(ctx,dst,f.RelativeName,f.Size,f.SHA256,func(w io.Writer)error{
			return readChecked(ctx,src,f.RelativeName,f.Size,f.SHA256,w)
		});err!=nil{return err}
	}
	s,err=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,s){return ErrStageRefused}
	intent:=publicationIntent{Version:1,Allocation:g,GalleryIdentity:r.GalleryIdentity,GalleryRoot:r.GalleryRoot,Files:e.Files}
	body,err:=json.Marshal(intent)
	if err!=nil||len(body)>maxSealRecordBytes{return ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"intent.json",body);err!=nil{return err}
	syncDirectory(dst);syncDirectory(p);syncDirectory(op);syncDirectory(a.root)
	s,err=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,s){return ErrStageRefused}
	_,err=readPublication(op,r,e)
	return err
}

// commonRoot obtains one rooted handle for exact owner-selected source
// and destination paths. No arbitrary requester path is interpreted.
// Root containment is not protection against arbitrary same-user
// modifications or bind mounts; integration must fence gallery changes.
func commonRoot(a,b string)(*os.Root,string,string,error){
	for base:=a;;base=filepath.Dir(base){
		x,ex:=filepath.Rel(base,a)
		y,ey:=filepath.Rel(base,b)
		if ex==nil&&ey==nil&&filepath.IsLocal(x)&&filepath.IsLocal(y){
			root,err:=os.OpenRoot(base)
			if err!=nil{return nil,"","",ErrStageRecovery}
			return root,x,y,nil
		}
		if filepath.Dir(base)==base{return nil,"","",ErrStageRefused}
	}
}

// inspectPlacements never repairs or retries. Equal bytes alone cannot
// establish that a destination was placed by this operation: require
// the exact retained inode/file and precisely two links after attempt.
func inspectPlacements(ctx context.Context,g workerproto.StageAllocation,e SealedEvidence,p,src,dst *os.Root)([]placement,error){
	attempted:=false
	if marker,err:=boundedRegularBytes(p,"attempt",64);err==nil&&string(marker)==g.TransferID{
		attempted=true
	}else if _,err:=p.Lstat("attempt");!errors.Is(err,os.ErrNotExist){return nil,ErrStageRecovery}
	out:=make([]placement,0,len(e.Files))
	for _,f:=range e.Files{
		name:=finalName(g,f)
		si,err:=src.Lstat(f.RelativeName)
		if err!=nil||!si.Mode().IsRegular(){return nil,ErrStageRecovery}
		di,err:=dst.Lstat(name)
		state:="prepared"
		links:=uint64(1)
		if err==nil{
			if !attempted||!di.Mode().IsRegular()||!os.SameFile(si,di){return nil,ErrStageRecovery}
			state="placed";links=2
		}else if !errors.Is(err,os.ErrNotExist){return nil,ErrStageRecovery
		}else if attempted{state="absent"}
		if err:=readCheckedLinks(ctx,src,f.RelativeName,f.Size,f.SHA256,io.Discard,links);err!=nil{return nil,err}
		if state=="placed"{
			if err:=readCheckedLinks(ctx,dst,name,f.Size,f.SHA256,io.Discard,2);err!=nil{return nil,err}
			current,err:=dst.Lstat(name)
			if err!=nil||!os.SameFile(si,current){return nil,ErrStageRecovery}
		}
		out=append(out,placement{ID:f.ID,Name:name,State:state})
	}
	return out,nil
}

// publicationRoots validates all durable prerequisites and opens exact
// private and gallery roots. Caller holds allocator mutex and owner lock.
func(a *Allocator)publicationRoots(ctx context.Context,g workerproto.StageAllocation)(record,SealedEvidence,*os.Root,*os.Root,*os.Root,error){
	var r record
	var e SealedEvidence
	fail:=func(err error)(record,SealedEvidence,*os.Root,*os.Root,*os.Root,error){return r,e,nil,nil,nil,err}
	e,err:=a.sealedLocked(ctx,g)
	if err!=nil{return fail(err)}
	s,err:=a.scope(ctx,g.Request)
	if err!=nil{return fail(err)}
	r,err=a.load(g.Request.OperationID)
	if err!=nil||!matches(r,s)||r.Allocation!=g{return fail(ErrStageRefused)}
	op,err:=openDir(a.root,g.Request.OperationID)
	if err!=nil{return fail(err)}
	defer op.Close()
	if _,err:=readPublication(op,r,e);err!=nil{return fail(err)}
	p,err:=openDir(op,"publication")
	if err!=nil{return fail(err)}
	src,err:=openDir(p,"media")
	if err!=nil{_ = p.Close();return fail(err)}
	_,di,err:=directory(r.GalleryRoot)
	if err!=nil{_ = src.Close();_ = p.Close();return fail(err)}
	dst,err:=os.OpenRoot(r.GalleryRoot)
	if err!=nil{_ = src.Close();_ = p.Close();return fail(ErrStageRefused)}
	current,err:=dst.Stat(".")
	if err!=nil||!os.SameFile(di,current){
		_ = dst.Close();_ = src.Close();_ = p.Close();return fail(ErrStageRefused)
	}
	return r,e,p,src,dst,nil
}

func digestBytes(body []byte)string{
	sum:=sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// publishPrepared makes at most one publication attempt. An exclusive,
// synced attempt marker precedes ANY link. Root.Link is atomic no-clobber
// on supporting filesystems. No rename-overwrite or direct visible copy
// fallback. Partial success or uncertain error is retained for inspection;
// even an all-placed result is not database commit or receipt.
func(a *Allocator)publishPrepared(ctx context.Context,g workerproto.StageAllocation)([]placement,error){
	a.mu.Lock();defer a.mu.Unlock()
	r,e,p,src,dst,err:=a.publicationRoots(ctx,g)
	if err!=nil{return nil,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if _,err:=p.Lstat("attempt");!errors.Is(err,os.ErrNotExist){return nil,ErrStageRecovery}
	if _,err:=inspectPlacements(ctx,g,e,p,src,dst);err!=nil{return nil,err}
	sourcePath:=filepath.Join(a.path,g.Request.OperationID,"publication","media")
	common,from,to,err:=commonRoot(sourcePath,r.GalleryRoot)
	if err!=nil{return nil,err}
	defer common.Close()
	rootsMatch:=func()bool{
		si,err:=src.Stat(".");if err!=nil{return false}
		di,err:=dst.Stat(".");if err!=nil{return false}
		x,err:=common.Lstat(from);if err!=nil||!x.IsDir()||!os.SameFile(si,x){return false}
		y,err:=common.Lstat(to);return err==nil&&y.IsDir()&&os.SameFile(di,y)
	}
	if !rootsMatch(){return nil,ErrStageRefused}
	if err:=writeOwnerBytes(ctx,p,"attempt",[]byte(g.TransferID));err!=nil{return nil,err}
	syncDirectory(p)
	for _,f:=range e.Files{
		s,err:=a.scope(ctx,g.Request)
		if err!=nil||!matches(r,s)||!rootsMatch(){return nil,ErrStageRefused}
		if err:=readChecked(ctx,src,f.RelativeName,f.Size,f.SHA256,io.Discard);err!=nil{return nil,err}
		if err:=common.Link(filepath.Join(from,f.RelativeName),filepath.Join(to,finalName(g,f)));err!=nil{return nil,ErrStageRecovery}
		syncDirectory(dst)
	}
	s,err:=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,s)||!rootsMatch(){return nil,ErrStageRefused}
	out,err:=inspectPlacements(ctx,g,e,p,src,dst)
	if err!=nil{return nil,err}
	for _,v:=range out{if v.State!="placed"{return nil,ErrStageRecovery}}
	return out,nil
}
func(a *Allocator)inspectPublication(ctx context.Context,g workerproto.StageAllocation)([]placement,error){
	a.mu.Lock();defer a.mu.Unlock()
	r,e,p,src,dst,err:=a.publicationRoots(ctx,g)
	if err!=nil{return nil,err}
	defer p.Close();defer src.Close();defer dst.Close()
	out,err:=inspectPlacements(ctx,g,e,p,src,dst)
	if err!=nil{return nil,err}
	s,err:=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,s){return nil,ErrStageRefused}
	return out,nil
}
