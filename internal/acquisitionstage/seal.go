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
	"slices"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

const maxSealRecordBytes = 1 << 20

// SealObserver is a trusted library/custody-side seam, never a wire
// assertion. It must establish exact current grant, original approved
// manifest and result evidence AFTER independently proving every writer
// stopped and no further activation is possible. The fixture observer
// is not the production implementation of that proof.
type SealObserver func(context.Context,workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error)

// SealedEvidence identifies verified private bytes. It is NOT a durable
// final import receipt, permission to commit, or proof of policy consent.
type SealedEvidence struct{
	Version int `json:"version"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	Files []acquisitionplan.VerifiedTransferFile `json:"files"`
}

func validSeal(e SealedEvidence,g workerproto.StageAllocation)bool{
	if e.Version!=1||e.Allocation!=g||!workerproto.ValidStageAllocation(g)||e.Files==nil||len(e.Files)>int(g.MaxFiles){return false}
	seen:=make(map[string]bool,len(e.Files))
	var total uint64
	for _,f:=range e.Files{
		if !validResultFile(f)||seen[f.ID]{return false}
		seen[f.ID]=true
		total+=uint64(f.Size)+uint64(f.SidecarSize)
		if total>g.MaxBytes{return false}
	}
	return true
}

// boundedRegularBytes reads an owner record without accepting symlinks,
// hardlinks, oversized data or changed file identity.
func boundedRegularBytes(root *os.Root,name string,limit int64)([]byte,error){
	info,err:=root.Lstat(name)
	if err!=nil||!info.Mode().IsRegular()||info.Size()<1||info.Size()>limit{return nil,ErrStageRecovery}
	f,err:=root.Open(name)
	if err!=nil{return nil,ErrStageRecovery}
	defer f.Close()
	actual,err:=f.Stat()
	if err!=nil||!os.SameFile(info,actual)||actual.Size()!=info.Size()||!singleLink(f,actual){return nil,ErrStageRecovery}
	raw,err:=io.ReadAll(io.LimitReader(f,limit+1))
	if err!=nil||int64(len(raw))!=info.Size(){return nil,ErrStageRecovery}
	after,err:=root.Lstat(name)
	if err!=nil||!after.Mode().IsRegular()||!os.SameFile(info,after)||after.Size()!=info.Size()||after.ModTime()!=info.ModTime()||!singleLink(f,after){return nil,ErrStageRecovery}
	return raw,nil
}

// readSeal validates structural state only. Never publish final media
// or permit cleanup from this alone; Sealed rechecks actual bytes.
func readSeal(operation *os.Root,g workerproto.StageAllocation)(SealedEvidence,error){
	var zero SealedEvidence
	dir,err:=openDir(operation,"sealed")
	if err!=nil{return zero,err}
	defer dir.Close()
	entries,err:=list(dir,4)
	if err!=nil||len(entries)<3||len(entries)>4{return zero,ErrStageRecovery}
	raw,err:=boundedRegularBytes(dir,"seal.json",maxSealRecordBytes)
	if err!=nil{return zero,err}
	var e SealedEvidence
	if json.Unmarshal(raw,&e)!=nil||!validSeal(e,g){return zero,ErrStageRecovery}
	canonical,err:=json.Marshal(e)
	if err!=nil||!bytes.Equal(canonical,raw){return zero,ErrStageRecovery}
	manifest,err:=dir.Lstat("manifest.json")
	if err!=nil||!manifest.Mode().IsRegular()||manifest.Size()<1||manifest.Size()>workerproto.MaxMetadataOutputBytes{return zero,ErrStageRecovery}
	media,err:=openDir(dir,"media")
	if err!=nil{return zero,err}
	defer media.Close()
	if err:=checkSealedInventory(operation,media,g,e);err!=nil{return zero,err}
	if len(entries)==4{
		if _,err:=readPolicyBinding(dir,e);err!=nil{return zero,ErrStageRecovery}
	}
	return e,nil
}

// Seal preserves both the original workspace and a separate verified
// library-owned copy. Its exclusive directory is a write-ahead marker:
// any interruption before a complete seal record requires reconciliation.
// A repeated Seal never overwrites/reuses partial files or re-invokes
// a downloader. Sealed queries existing verified evidence instead.
func(a *Allocator)Seal(ctx context.Context,g workerproto.StageAllocation,observe SealObserver)(SealedEvidence,error){
	var zero SealedEvidence
	a.mu.Lock()
	defer a.mu.Unlock()
	if observe==nil||!workerproto.ValidStageAllocation(g){return zero,ErrStageRefused}
	s,err:=a.scope(ctx,g.Request)
	if err!=nil{return zero,err}
	r,err:=a.load(g.Request.OperationID)
	if err!=nil||r.Allocation!=g||!matches(r,s){return zero,ErrStageRefused}
	operation,err:=openDir(a.root,g.Request.OperationID)
	if err!=nil{return zero,err}
	defer operation.Close()
	if _,err:=operation.Lstat("sealed");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	manifest,files,err:=observe(ctx,g)
	if err!=nil||ctx.Err()!=nil||len(manifest)>workerproto.MaxMetadataOutputBytes||len(files)>int(g.MaxFiles){return zero,ErrStageRefused}
	manifest=bytes.Clone(manifest)
	files=append([]acquisitionplan.VerifiedTransferFile{},files...)
	e:=SealedEvidence{Version:1,Allocation:g,Files:files}
	if !validSeal(e,g){return zero,ErrStageRefused}
	if _,err:=acquisitionplan.NewStrictManifestVerifier(g.Request.Transfer,manifest);err!=nil{return zero,ErrStageRefused}
	current,err:=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,current){return zero,ErrStageRefused}
	workspace,err:=openDir(operation,g.TransferID)
	if err!=nil{return zero,err}
	defer workspace.Close()
	// The actual strict command workspace layout is fixed. Do not
	// treat arbitrary files or requester-chosen paths as media.
	entries,err:=list(workspace,3)
	if err!=nil||len(entries)!=3{return zero,ErrStageRecovery}
	raw,err:=boundedRegularBytes(workspace,"strict-blacklist.json",workerproto.MaxMetadataOutputBytes)
	if err!=nil||!bytes.Equal(raw,manifest){return zero,ErrStageRecovery}
	probe,err:=workspace.Lstat("strict-blacklist-probe.json")
	if err!=nil||!probe.Mode().IsRegular(){return zero,ErrStageRecovery}
	src,err:=openDir(workspace,"media")
	if err!=nil{return zero,err}
	defer src.Close()
	var copyBytes uint64
	for _,f:=range files{copyBytes+=uint64(f.Size)+uint64(f.SidecarSize)}
	free,err:=a.freeBytes(a.path)
	if err!=nil||free<copyBytes+uint64(len(manifest))+maxSealRecordBytes||!a.live(){return zero,ErrStageLimit}
	if err:=operation.Mkdir("sealed",0700);err!=nil{return zero,ErrStageRecovery}
	sealed,err:=openDir(operation,"sealed")
	if err!=nil{return zero,err}
	defer sealed.Close()
	if err:=sealed.Mkdir("media",0700);err!=nil{return zero,ErrStageRecovery}
	dst,err:=openDir(sealed,"media")
	if err!=nil{return zero,err}
	defer dst.Close()
	copied,err:=copySealedMedia(ctx,g,manifest,files,src,dst)
	if err!=nil{return zero,err}
	e.Files=append([]acquisitionplan.VerifiedTransferFile{},copied...)
	if err:=writeChecked(ctx,sealed,"manifest.json",int64(len(manifest)),g.Request.Transfer.ManifestSHA256,func(w io.Writer)error{
		_,err:=io.Copy(w,bytes.NewReader(manifest));return err
	});err!=nil{return zero,err}
	current,err=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,current){return zero,ErrStageRefused}
	body,err:=json.Marshal(e)
	if err!=nil||len(body)>maxSealRecordBytes{return zero,ErrStageRecovery}
	sum:=sha256.Sum256(body)
	if err:=writeChecked(ctx,sealed,"seal.json",int64(len(body)),hex.EncodeToString(sum[:]),func(w io.Writer)error{
		_,err:=io.Copy(w,bytes.NewReader(body));return err
	});err!=nil{return zero,err}
	syncDirectory(sealed);syncDirectory(operation);syncDirectory(a.root)
	current,err=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,current){return zero,ErrStageRefused}
	stored,err:=readSeal(operation,g)
	if err!=nil||!slices.Equal(e.Files,stored.Files){return zero,ErrStageRecovery}
	return stored,nil
}

// Sealed reconciles existing private evidence after a lost reply or
// reopen. It rechecks exact current library scope and every stored byte.
// The original writer state must still be reconciled by custody; a
// successful query is neither cleanup proof nor final commit permission.
func(a *Allocator)Sealed(ctx context.Context,g workerproto.StageAllocation)(SealedEvidence,error){
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sealedLocked(ctx,g)
}
func(a *Allocator)sealedLocked(ctx context.Context,g workerproto.StageAllocation)(SealedEvidence,error){
	return a.sealedModeLocked(ctx,g,false)
}
func(a *Allocator)sealedModeLocked(ctx context.Context,g workerproto.StageAllocation,retired bool)(SealedEvidence,error){
	var zero SealedEvidence
	if !workerproto.ValidStageAllocation(g){return zero,ErrStageRefused}
	s,err:=a.scope(ctx,g.Request)
	if err!=nil{return zero,err}
	r,err:=a.load(g.Request.OperationID)
	if err!=nil||r.Allocation!=g||!matches(r,s){return zero,ErrStageRefused}
	operation,err:=openDir(a.root,g.Request.OperationID)
	if err!=nil{return zero,err}
	defer operation.Close()
	e,err:=readSeal(operation,g)
	if err!=nil{return zero,err}
	sealed,err:=openDir(operation,"sealed")
	if err!=nil{return zero,err}
	defer sealed.Close()
	manifest,err:=boundedRegularBytes(sealed,"manifest.json",workerproto.MaxMetadataOutputBytes)
	if err!=nil{return zero,err}
	verifier,err:=acquisitionplan.NewStrictManifestVerifier(g.Request.Transfer,manifest)
	if err!=nil{return zero,ErrStageRecovery}
	if _,err:=sealed.Lstat("policy");err==nil{
		if _,err:=verifyBoundPolicy(sealed,e,manifest);err!=nil{return zero,err}
	}else if !errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	media,err:=openDir(sealed,"media")
	if err!=nil{return zero,err}
	defer media.Close()
	for _,f:=range e.Files{
		var meta bytes.Buffer
		if err:=readChecked(ctx,media,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256,&meta);err!=nil{return zero,err}
		if err:=verifier.VerifySidecar(f.ID,meta.Bytes());err!=nil{return zero,ErrStageRecovery}
		if retired{
			if _,err:=media.Lstat(f.RelativeName);!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
		}else if err:=readChecked(ctx,media,f.RelativeName,f.Size,f.SHA256,io.Discard);err!=nil{return zero,err}
	}
	current,err:=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,current){return zero,ErrStageRefused}
	if retired{
		if err:=checkRetiredSeal(operation,media,r,e);err!=nil{return zero,err}
	}else if !exactInventory(media,e.Files){return zero,ErrStageRecovery}
	return e,nil
}
