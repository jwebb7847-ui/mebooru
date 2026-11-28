// Package acquisitionstage implements isolated library-owned staging
// candidates. It is not mounted by the production application.
package acquisitionstage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/desktop"
)

var ErrStageRefused = errors.New("managed staging refused")
var ErrStageRecovery = errors.New("managed staging requires reconciliation")
var ErrStageLimit = errors.New("managed staging reservation limit")

const maxRecordBytes = 128 << 10
const maxAllocations = 1024
const maxReservedBytes = uint64(16) << 30

// Scope is observed independently by the library. GalleryIdentity is an
// owner-maintained stable identity, not a gallery display name. GalleryRoots
// must include every scanned gallery, including the exact destination.
// Authority must remain bounded, must not reenter the allocator and must
// refuse stale job/policy/lifecycle state. No requester can supply Scope.
type Scope struct {
	GalleryIdentity string
	GalleryRoot string
	GalleryRoots []string
	MaxFiles uint32
	MaxBytes uint64
}
type Authority func(context.Context,workerproto.StageAllocateRequest)(Scope,error)

type record struct {
	Version int `json:"version"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	GalleryIdentity string `json:"gallery_identity"`
	GalleryRoot string `json:"gallery_root"`
	StageRoot string `json:"stage_root"`
}

// Allocator is local to the single cooperative profile owner. Caller must
// hold exclusive profile ownership for its lifetime; this mutex is not an
// interprocess lock. Root must be an existing owner-selected directory.
// Completed closure can release active capacity; retained records remain bounded
// by maxAllocations and are never recycled automatically. No implicit cleanup
// or launch occurs here. Ambiguous records reserve capacity and
// block further allocation rather than being removed or guessed at.
type Allocator struct {
	mu sync.Mutex
	root *os.Root
	path string
	info os.FileInfo
	authority Authority
	maxCount int
	maxBytes uint64
	freeBytes func(string)(uint64,error)
}

func Open(path string,maxCount int,maxBytes uint64,authority Authority)(*Allocator,error){
	if authority==nil||maxCount<1||maxCount>maxAllocations||maxBytes<1||maxBytes>maxReservedBytes{return nil,ErrStageRefused}
	canonical,info,err:=directory(path)
	if err!=nil{return nil,err}
	r,err:=os.OpenRoot(canonical)
	if err!=nil{return nil,ErrStageRefused}
	actual,err:=r.Stat(".")
	if err!=nil||!os.SameFile(info,actual){_ = r.Close();return nil,ErrStageRefused}
	return &Allocator{root:r,path:canonical,info:info,authority:authority,maxCount:maxCount,maxBytes:maxBytes,freeBytes:diskFree},nil
}
func(a *Allocator)Close()error{
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.root==nil{return nil}
	err:=a.root.Close()
	a.root=nil
	return err
}

// Canonical absolute directories are required; the trusted owner may
// resolve OS aliases before calling. Refuse symlink/reparse aliases, not
// just lexical traversal. This does not fence hostile same-user changes.
func directory(path string)(string,os.FileInfo,error){
	if !filepath.IsAbs(path)||filepath.Clean(path)!=path{return "",nil,ErrStageRefused}
	actual,err:=filepath.EvalSymlinks(path)
	if err!=nil||actual!=path{return "",nil,ErrStageRefused}
	info,err:=os.Lstat(path)
	if err!=nil||!info.IsDir(){return "",nil,ErrStageRefused}
	return actual,info,nil
}

// Check actual ancestor directory identities in both directions, not
// merely string prefixes (case-insensitive volumes and aliases).
func contains(ancestor os.FileInfo,path string)(bool,error){
	for{
		info,err:=os.Stat(path)
		if err!=nil{return false,ErrStageRefused}
		if os.SameFile(ancestor,info){return true,nil}
		parent:=filepath.Dir(path)
		if parent==path{return false,nil}
		path=parent
	}
}
func(a *Allocator)live()bool{
	if a.root==nil{return false}
	_,info,err:=directory(a.path)
	return err==nil&&os.SameFile(a.info,info)
}
func(a *Allocator)scope(ctx context.Context,q workerproto.StageAllocateRequest)(Scope,error){
	if ctx.Err()!=nil||!a.live()||!workerproto.ValidStageAllocate(q){return Scope{},ErrStageRefused}
	s,err:=a.authority(ctx,q)
	if err!=nil||ctx.Err()!=nil||s.GalleryIdentity==""||len(s.GalleryIdentity)>1024||len(s.GalleryRoots)==0||len(s.GalleryRoots)>1024||
		s.MaxFiles==0||s.MaxFiles>q.Transfer.Metadata.Last-q.Transfer.Metadata.First+1||
		s.MaxBytes==0||s.MaxBytes>workerproto.MaxStageTotalBytes{return Scope{},ErrStageRefused}
	dest,destInfo,err:=directory(s.GalleryRoot)
	if err!=nil{return Scope{},err}
	found:=false
	for _,path:=range s.GalleryRoots{
		path,info,err:=directory(path)
		if err!=nil{return Scope{},err}
		if os.SameFile(destInfo,info){found=true}
		inside,err:=contains(info,a.path)
		if err!=nil||inside{return Scope{},ErrStageRefused}
		inside,err=contains(a.info,path)
		if err!=nil||inside{return Scope{},ErrStageRefused}
	}
	if !found||!a.live()||!a.sameDestinationVolume(dest,destInfo){return Scope{},ErrStageRefused}
	s.GalleryRoot=dest
	return s,nil
}
// Same-volume is a point-in-time prerequisite of this first candidate.
// Unknown or different device/volume refuses allocation; no unsafe
// cross-volume fallback. The owner must still qualify actual filesystem
// capabilities and durable identity before final publication.
func(a *Allocator)sameDestinationVolume(path string,info os.FileInfo)bool{
	stage,err:=a.root.Open(".")
	if err!=nil{return false}
	defer stage.Close()
	dest,err:=os.Open(path)
	if err!=nil{return false}
	defer dest.Close()
	si,err:=stage.Stat()
	if err!=nil||!os.SameFile(a.info,si){return false}
	di,err:=dest.Stat()
	if err!=nil||!os.SameFile(info,di){return false}
	return sameVolume(stage,dest)
}
func diskFree(path string)(uint64,error){
	_,free,err:=desktop.DiskUsage(path)
	return free,err
}

// The payload reservation includes media/sidecars. Allow for both the
// original, sealed and publication copies, two manifests, one policy artifact and bounded
// owner records/probe overhead. Conservative free-space preflight only:
// existing reservations may already occupy space, and other writers may
// consume the free bytes later. This does not enforce live disk quotas.
const reservationOverhead = uint64(3*workerproto.MaxMetadataOutputBytes+8<<20+2*maxRelationshipRecord+2*maxCollectionRecord+2*maxNamingRecord+2*maxExtrasRecord+maxComposedReceipt+maxHeldComposition+maxHeldReceipt+maxRestoredReceipt+2*maxWorkspaceReclaimRecord+2*maxPreparedReleaseRecord+2*maxSealedRetirementRecord+maxClosureRecord+maxPlannedAttemptBytes+maxExistingTargetAnchorBytes+maxExistingLinkAttemptBytes+maxExistingLandingAttemptBytes)
func(a *Allocator)spaceFor(count int,payload uint64)bool{
	if count<0||count>maxAllocations||payload>maxReservedBytes||a.freeBytes==nil{return false}
	required:=3*payload+uint64(count)*reservationOverhead
	free,err:=a.freeBytes(a.path)
	return err==nil&&free>=required&&a.live()
}
func matches(r record,s Scope)bool{
	return r.GalleryIdentity==s.GalleryIdentity&&r.GalleryRoot==s.GalleryRoot&&
		r.Allocation.MaxFiles==s.MaxFiles&&r.Allocation.MaxBytes==s.MaxBytes
}
func validID(id string)bool{
	b,err:=hex.DecodeString(id)
	return err==nil&&len(b)==16&&hex.EncodeToString(b)==id&&id!="00000000000000000000000000000000"
}
func readRecord(dir *os.Root)(record,error){
	var r record
	info,err:=dir.Lstat("allocation.json")
	if err!=nil||!info.Mode().IsRegular()||info.Size()<1||info.Size()>maxRecordBytes{return r,ErrStageRecovery}
	f,err:=dir.Open("allocation.json")
	if err!=nil{return r,ErrStageRecovery}
	defer f.Close()
	actual,err:=f.Stat()
	if err!=nil||!os.SameFile(info,actual)||!singleLink(f,actual){return r,ErrStageRecovery}
	raw,err:=io.ReadAll(io.LimitReader(f,maxRecordBytes+1))
	if err!=nil||int64(len(raw))!=info.Size()||json.Unmarshal(raw,&r)!=nil{return record{},ErrStageRecovery}
	canonical,err:=json.Marshal(r)
	if err!=nil||!bytes.Equal(raw,canonical)||r.Version!=1||r.GalleryIdentity==""||
		!workerproto.ValidStageAllocation(r.Allocation){return record{},ErrStageRecovery}
	after,err:=dir.Lstat("allocation.json")
	if err!=nil||!os.SameFile(info,after)||after.Size()!=info.Size()||after.ModTime()!=info.ModTime()||!singleLink(f,after){return record{},ErrStageRecovery}
	return r,nil
}
func openDir(root *os.Root,name string)(*os.Root,error){
	info,err:=root.Lstat(name)
	if err!=nil||!info.IsDir(){return nil,ErrStageRecovery}
	dir,err:=root.OpenRoot(name)
	if err!=nil{return nil,ErrStageRecovery}
	actual,err:=dir.Stat(".")
	if err!=nil||!os.SameFile(info,actual){_ = dir.Close();return nil,ErrStageRecovery}
	return dir,nil
}
func list(root *os.Root,limit int)([]os.DirEntry,error){
	f,err:=root.Open(".")
	if err!=nil{return nil,ErrStageRecovery}
	defer f.Close()
	entries,err:=f.ReadDir(limit+1)
	if err!=nil&&err!=io.EOF||len(entries)>limit{return nil,ErrStageRecovery}
	return entries,nil
}
func(a *Allocator)load(id string)(record,error){
	var zero record
	if !validID(id){return zero,ErrStageRecovery}
	dir,err:=openDir(a.root,id)
	if err!=nil{return zero,err}
	defer dir.Close()
	r,err:=readRecord(dir)
	if err!=nil||r.Allocation.Request.OperationID!=id||r.StageRoot!=a.path{return zero,ErrStageRecovery}
	entries,err:=list(dir,4)
	if err!=nil||len(entries)<2||len(entries)>4{return zero,ErrStageRecovery}
	if len(entries)>=3{
		e,err:=readSeal(dir,r.Allocation)
		if err!=nil{return zero,ErrStageRecovery}
		if len(entries)==4{
			if _,err:=readPublication(dir,r,e);err!=nil{return zero,ErrStageRecovery}
		}
	}
	workspace,err:=openDir(dir,r.Allocation.TransferID)
	if err!=nil{return zero,err}
	_ = workspace.Close()
	return r,nil
}
func(a *Allocator)inventory(ctx context.Context)(map[string]record,uint64,error){
	records,_,reserved,err:=a.inventoryWithClosure(ctx)
	return records,reserved,err
}
func(a *Allocator)inventoryWithClosure(ctx context.Context)(map[string]record,map[string]bool,uint64,error){
	entries,err:=list(a.root,maxAllocations)
	if err!=nil{return nil,nil,0,err}
	records:=make(map[string]record,len(entries))
	closed:=make(map[string]bool)
	transfers:=make(map[string]bool,len(entries))
	var reserved uint64
	for _,entry:=range entries{
		if ctx.Err()!=nil{return nil,nil,0,ErrStageRefused}
		if !entry.IsDir(){return nil,nil,0,ErrStageRecovery}
		r,err:=a.load(entry.Name())
		if err!=nil||transfers[r.Allocation.TransferID]{return nil,nil,0,ErrStageRecovery}
		transfers[r.Allocation.TransferID]=true
		done,charge,err:=a.closedCharge(ctx,r)
		if err!=nil{return nil,nil,0,err}
		if done{closed[entry.Name()]=true}
		reserved+=charge
		records[entry.Name()]=r
	}
	return records,closed,reserved,nil
}
func writeRecord(dir *os.Root,r record)error{
	body,err:=json.Marshal(r)
	if err!=nil||len(body)>maxRecordBytes{return ErrStageRecovery}
	f,err:=dir.OpenFile("allocation.json",os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600)
	if err!=nil{return ErrStageRecovery}
	n,err:=f.Write(body)
	if err==nil&&n!=len(body){err=io.ErrShortWrite}
	if err==nil{err=f.Sync()}
	if closeErr:=f.Close();err==nil{err=closeErr}
	if err!=nil{return ErrStageRecovery}
	return nil
}
func syncDirectory(r *os.Root){
	// Directory sync is best-effort on platforms that do not support it.
	// No power-loss durability guarantee is made by this candidate.
	if f,err:=r.Open(".");err==nil{_ = f.Sync();_ = f.Close()}
}

// Allocate reconciles an exact repeated request to its existing reservation.
// It does NOT authorize starting/restarting a downloader. No partial
// allocation is deleted, and a changed scope never gets a fresh workspace.
func(a *Allocator)Allocate(ctx context.Context,q workerproto.StageAllocateRequest)(workerproto.StageAllocation,error){
	a.mu.Lock()
	defer a.mu.Unlock()
	s,err:=a.scope(ctx,q)
	if err!=nil{return workerproto.StageAllocation{},err}
	records,closed,reserved,err:=a.inventoryWithClosure(ctx)
	if err!=nil{return workerproto.StageAllocation{},err}
	if r,ok:=records[q.OperationID];ok{
		if closed[q.OperationID]||r.Allocation.Request!=q||!matches(r,s){return workerproto.StageAllocation{},ErrStageRefused}
		current,err:=a.scope(ctx,q)
		if err!=nil||!matches(r,current){return workerproto.StageAllocation{},ErrStageRefused}
		return r.Allocation,nil
	}
	if len(records)>=maxAllocations||len(records)-len(closed)>=a.maxCount||reserved>a.maxBytes||s.MaxBytes>a.maxBytes-reserved||!a.spaceFor(len(records)+1,reserved+s.MaxBytes){return workerproto.StageAllocation{},ErrStageLimit}
	var id [16]byte
	if _,err:=rand.Read(id[:]);err!=nil{return workerproto.StageAllocation{},ErrStageRefused}
	grant:=workerproto.StageAllocation{Request:q,TransferID:hex.EncodeToString(id[:]),MaxFiles:s.MaxFiles,MaxBytes:s.MaxBytes}
	if !workerproto.ValidStageAllocation(grant){return workerproto.StageAllocation{},ErrStageRefused}
	for _,r:=range records{if r.Allocation.TransferID==grant.TransferID{return workerproto.StageAllocation{},ErrStageRecovery}}
	r:=record{Version:1,Allocation:grant,GalleryIdentity:s.GalleryIdentity,GalleryRoot:s.GalleryRoot,StageRoot:a.path}
	if ctx.Err()!=nil||!a.live(){return workerproto.StageAllocation{},ErrStageRefused}
	// Mkdir is exclusive: another owner/corrupt record cannot be overwritten.
	if err:=a.root.Mkdir(q.OperationID,0700);err!=nil{return workerproto.StageAllocation{},ErrStageRecovery}
	dir,err:=openDir(a.root,q.OperationID)
	if err!=nil{return workerproto.StageAllocation{},err}
	defer dir.Close()
	if err:=dir.Mkdir(grant.TransferID,0700);err!=nil{return workerproto.StageAllocation{},ErrStageRecovery}
	if err:=writeRecord(dir,r);err!=nil{return workerproto.StageAllocation{},err}
	syncDirectory(dir);syncDirectory(a.root)
	current,err:=a.scope(ctx,q)
	if err!=nil||!matches(r,current){return workerproto.StageAllocation{},ErrStageRefused}
	stored,err:=a.load(q.OperationID)
	if err!=nil||stored!=r{return workerproto.StageAllocation{},ErrStageRecovery}
	return grant,nil
}

// Workspace resolves only an exact existing allocation under current
// library scope. It is a local diagnostic path, not a wire response or
// writer grant. Future custody must independently reserve execution,
// prove no old writers, and bind a rooted workspace handle.
func(a *Allocator)Workspace(ctx context.Context,grant workerproto.StageAllocation)(string,error){
	a.mu.Lock()
	defer a.mu.Unlock()
	if !workerproto.ValidStageAllocation(grant){return "",ErrStageRefused}
	s,err:=a.scope(ctx,grant.Request)
	if err!=nil{return "",err}
	r,err:=a.load(grant.Request.OperationID)
	if err!=nil||r.Allocation!=grant||!matches(r,s){return "",ErrStageRefused}
	dir,err:=openDir(a.root,grant.Request.OperationID)
	if err!=nil{return "",err}
	defer dir.Close()
	if _,err:=dir.Lstat("sealed");!errors.Is(err,os.ErrNotExist){return "",ErrStageRecovery}
	path:=filepath.Join(a.path,grant.Request.OperationID,grant.TransferID)
	if _,_,err:=directory(path);err!=nil||!a.live()||ctx.Err()!=nil{return "",ErrStageRefused}
	return path,nil
}
