package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// Separate owner/custody authorization, not an inference from a receipt.
// Must independently establish that the exact original workspace has
// no live writer, no further activation is possible, and this specific
// reclamation is still authorized. Caller holds exclusive owner lifetime.
type reclaimAuthority func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error
const maxWorkspaceReclaimRecord = 256 << 10

type workspaceFile struct{
	Name string `json:"name"`
	Size int64 `json:"size"`
	SHA256 string `json:"sha256"`
	Identity string `json:"identity"`
}
type workspaceReclaimAttempt struct{
	Version int `json:"version"`
	Witness terminalReceiptWitness `json:"witness"`
	WorkspaceIdentity string `json:"workspace_identity"`
	MediaIdentity string `json:"media_identity"`
	Manifest workspaceFile `json:"manifest"`
	Probe workspaceFile `json:"probe"`
	Files [2]workspaceFile `json:"files"`
}
type workspaceReclaimResult struct{
	Version int `json:"version"`
	AttemptSHA256 string `json:"attempt_sha256"`
	RemovedBytes int64 `json:"removed_bytes"`
}
func rootIdentity(root *os.Root)(string,error){
	f,err:=root.Open(".")
	if err!=nil{return "",ErrStageRecovery}
	defer f.Close()
	i,err:=f.Stat()
	if err!=nil||!i.IsDir(){return "",ErrStageRecovery}
	id,ok:=diskFileIdentity(f,i)
	if !ok||id==""||len(id)>128{return "",ErrStageRecovery}
	return id,nil
}
func workspaceRecord(ctx context.Context,root *os.Root,name string,limit int64)(workspaceFile,error){
	var zero workspaceFile
	raw,err:=boundedRegularBytes(root,name,limit)
	if err!=nil{return zero,err}
	return workspaceFileFor(ctx,root,name,int64(len(raw)),digestBytes(raw))
}
func workspaceFileFor(ctx context.Context,root *os.Root,name string,size int64,hash string)(workspaceFile,error){
	var zero workspaceFile
	id,err:=namingFile(ctx,root,name,acquisitionplan.VerifiedTransferFile{Size:size,SHA256:hash},1)
	if err!=nil{return zero,err}
	return workspaceFile{Name:name,Size:size,SHA256:hash,Identity:id},nil
}
func validWorkspaceFile(v workspaceFile,name string,size int64,hash string)bool{
	return v.Name==name&&v.Size==size&&v.SHA256==hash&&validDigest(hash)&&size>0&&v.Identity!=""&&len(v.Identity)<=128
}
func receiptRecordName(kind string)(string,int64){
	switch kind{
	case composedReceiptKind:return "composed-receipt.json",maxComposedReceipt
	case heldReceiptKind:return "held-receipt.json",maxHeldReceipt
	case restoredReceiptKind:return "restored-receipt.json",maxRestoredReceipt
	default:return "",0
	}
}
func readWorkspaceReclaimAttempt(p *os.Root,r record,e SealedEvidence)(workspaceReclaimAttempt,[]byte,error){
	var zero workspaceReclaimAttempt
	raw,err:=boundedRegularBytes(p,"workspace-reclaim-attempt.json",maxWorkspaceReclaimRecord)
	if err!=nil{return zero,nil,err}
	var m workspaceReclaimAttempt
	if json.Unmarshal(raw,&m)!=nil||m.Version!=1||len(e.Files)!=1||m.Witness.Allocation!=r.Allocation||m.Witness.GalleryIdentity!=r.GalleryIdentity||
		!validDigest(m.Witness.PolicySHA256)||!validDigest(m.Witness.ReceiptSHA256)||m.Witness.PathID<=0||m.Witness.FileIdentity==""||len(m.Witness.FileIdentity)>128||
		!validNamingRelative(m.Witness.Row.Name)||m.Witness.Row.ImageID<=0||m.WorkspaceIdentity==""||len(m.WorkspaceIdentity)>128||
		m.MediaIdentity==""||len(m.MediaIdentity)>128||m.MediaIdentity==m.WorkspaceIdentity{return zero,nil,ErrStageRecovery}
	f:=e.Files[0]
	if m.Witness.Row.PostID!=f.ID||m.Witness.Row.Size!=f.Size||m.Witness.Row.SHA256!=f.SHA256||
		!validWorkspaceFile(m.Files[0],f.RelativeName,f.Size,f.SHA256)||
		!validWorkspaceFile(m.Files[1],f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256)||
		!validWorkspaceFile(m.Manifest,"strict-blacklist.json",m.Manifest.Size,r.Allocation.Request.Transfer.ManifestSHA256)||
		!validWorkspaceFile(m.Probe,"strict-blacklist-probe.json",m.Probe.Size,m.Probe.SHA256)||
		m.Manifest.Size>workerproto.MaxMetadataOutputBytes||m.Probe.Size>workerproto.MaxMetadataOutputBytes{return zero,nil,ErrStageRecovery}
	seen:=map[string]bool{m.Witness.FileIdentity:true,m.WorkspaceIdentity:true,m.MediaIdentity:true}
	for _,v:=range []workspaceFile{m.Files[0],m.Files[1],m.Manifest,m.Probe}{
		if seen[v.Identity]{return zero,nil,ErrStageRecovery};seen[v.Identity]=true
	}
	name,limit:=receiptRecordName(m.Witness.Kind)
	if name==""{return zero,nil,ErrStageRecovery}
	receipt,err:=boundedRegularBytes(p,name,limit)
	if err!=nil||digestBytes(receipt)!=m.Witness.ReceiptSHA256{return zero,nil,ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,nil,ErrStageRecovery}
	return m,raw,nil
}
func expectedWorkspaceReclaimResult(m workspaceReclaimAttempt,raw []byte)workspaceReclaimResult{
	return workspaceReclaimResult{Version:1,AttemptSHA256:digestBytes(raw),RemovedBytes:m.Files[0].Size+m.Files[1].Size}
}
func readWorkspaceReclaimResult(p *os.Root,m workspaceReclaimAttempt,raw []byte)(workspaceReclaimResult,error){
	var zero workspaceReclaimResult
	want:=expectedWorkspaceReclaimResult(m,raw)
	body,err:=boundedRegularBytes(p,"workspace-reclaim-result.json",maxWorkspaceReclaimRecord)
	canonical,e:=json.Marshal(want)
	if err!=nil||e!=nil||!bytes.Equal(body,canonical){return zero,ErrStageRecovery}
	return want,nil
}

// A partial deletion is exactly a prefix of the two recorded entries.
// No unexpected files, directory replacement, or changed survivors.
// Structural inventory is cheap; the explicit reclaim observer hashes
// every remaining payload before authorizing another unlink.
func workspaceProgress(ctx context.Context,workspace,media *os.Root,m workspaceReclaimAttempt,verifyBytes bool)(int,error){
	wid,err:=rootIdentity(workspace);mid,e:=rootIdentity(media)
	if err!=nil||e!=nil||wid!=m.WorkspaceIdentity||mid!=m.MediaIdentity{return 0,ErrStageRecovery}
	entries,err:=list(workspace,3)
	if err!=nil||len(entries)!=3{return 0,ErrStageRecovery}
	for _,v:=range entries{
		switch v.Name(){
		case "media":if !v.IsDir(){return 0,ErrStageRecovery}
		case m.Manifest.Name,m.Probe.Name:if !v.Type().IsRegular(){return 0,ErrStageRecovery}
		default:return 0,ErrStageRecovery
		}
	}
	for _,v:=range []workspaceFile{m.Manifest,m.Probe}{
		got,err:=workspaceRecord(ctx,workspace,v.Name,workerproto.MaxMetadataOutputBytes)
		if err!=nil||got!=v{return 0,ErrStageRecovery}
	}
	items,err:=list(media,2)
	if err!=nil{return 0,err}
	removed:=2-len(items)
	for i,v:=range m.Files{
		info,err:=media.Lstat(v.Name)
		if i<removed{
			if !errors.Is(err,os.ErrNotExist){return 0,ErrStageRecovery}
			continue
		}
		if err!=nil||!info.Mode().IsRegular()||info.Size()!=v.Size{return 0,ErrStageRecovery}
		if verifyBytes{
			got,err:=workspaceFileFor(ctx,media,v.Name,v.Size,v.SHA256)
			if err!=nil||got!=v{return 0,ErrStageRecovery}
		}else{
			file,err:=media.Open(v.Name)
			if err!=nil{return 0,ErrStageRecovery}
			actual,err:=file.Stat()
			if err!=nil||!os.SameFile(info,actual)||!singleLink(file,actual){file.Close();return 0,ErrStageRecovery}
			id,ok:=diskFileIdentity(file,actual);file.Close()
			if !ok||id!=v.Identity{return 0,ErrStageRecovery}
		}
	}
	return removed,nil
}
func validateWorkspaceReclaimRecords(op,p *os.Root,r record,e SealedEvidence,hasResult bool)error{
	m,raw,err:=readWorkspaceReclaimAttempt(p,r,e)
	if err!=nil{return err}
	w,err:=openDir(op,r.Allocation.TransferID)
	if err!=nil{return err};defer w.Close()
	media,err:=openDir(w,"media")
	if err!=nil{return err};defer media.Close()
	removed,err:=workspaceProgress(context.Background(),w,media,m,false)
	if err!=nil{return err}
	if hasResult{
		if removed!=2{return ErrStageRecovery}
		_,err:=readWorkspaceReclaimResult(p,m,raw)
		return err
	}
	return nil
}

// Begin only after independently observing a terminal receipt and
// explicit current custody/owner permission. Retain exact original
// manifest and probe, sealed media/sidecar and publication evidence.
// No allocator reservation release at this first reclaim boundary.
func(a *Allocator)beginWorkspaceReclaimLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize reclaimAuthority)error{
	if authorize==nil{return ErrStageRefused}
	witness,err:=a.observeTerminalReceiptLocked(ctx,grant,g,naming,extras)
	if err!=nil{return err}
	if err:=authorize(ctx,grant,witness);err!=nil{return ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err};defer p.Close();defer src.Close();defer dst.Close()
	for _,name:=range []string{"workspace-reclaim-attempt.json","workspace-reclaim-result.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	op,err:=openDir(a.root,grant.Request.OperationID)
	if err!=nil{return err};defer op.Close()
	w,err:=openDir(op,grant.TransferID)
	if err!=nil{return err};defer w.Close()
	media,err:=openDir(w,"media")
	if err!=nil{return err};defer media.Close()
	if len(e.Files)!=1||!exactInventory(media,e.Files){return ErrStageRecovery}
	f:=e.Files[0]
	m:=workspaceReclaimAttempt{Version:1,Witness:witness}
	m.WorkspaceIdentity,err=rootIdentity(w)
	if err!=nil{return err}
	m.MediaIdentity,err=rootIdentity(media)
	if err!=nil{return err}
	m.Manifest,err=workspaceRecord(ctx,w,"strict-blacklist.json",workerproto.MaxMetadataOutputBytes)
	if err!=nil||m.Manifest.SHA256!=grant.Request.Transfer.ManifestSHA256{return ErrStageRecovery}
	m.Probe,err=workspaceRecord(ctx,w,"strict-blacklist-probe.json",workerproto.MaxMetadataOutputBytes)
	if err!=nil{return err}
	m.Files[0],err=workspaceFileFor(ctx,media,f.RelativeName,f.Size,f.SHA256)
	if err!=nil{return err}
	m.Files[1],err=workspaceFileFor(ctx,media,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256)
	if err!=nil{return err}
	if progress,err:=workspaceProgress(ctx,w,media,m,true);err!=nil||progress!=0{return ErrStageRecovery}
	again,err:=a.observeTerminalReceiptLocked(ctx,grant,g,naming,extras)
	if err!=nil||again!=witness{return ErrStageRecovery}
	if err:=authorize(ctx,grant,witness);err!=nil{return ErrStageRefused}
	if !a.livePublicationRoots(grant,r,p,src,dst)||ctx.Err()!=nil{return ErrStageRefused}
	if progress,err:=workspaceProgress(ctx,w,media,m,true);err!=nil||progress!=0{return ErrStageRecovery}
	body,err:=json.Marshal(m)
	if err!=nil||len(body)>maxWorkspaceReclaimRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"workspace-reclaim-attempt.json",body);err!=nil{return err}
	syncDirectory(p)
	return nil
}
func(a *Allocator)reclaimWorkspace(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize reclaimAuthority)(workspaceReclaimResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	if err:=a.beginWorkspaceReclaimLocked(ctx,grant,g,naming,extras,authorize);err!=nil{return workspaceReclaimResult{},err}
	return a.resumeWorkspaceReclaimLocked(ctx,grant,g,naming,extras,authorize,true)
}
// Explicit resume, not implicit replay by Allocate, Seal or receipt
// observation. Reconcile exact prefix and reauthorize each next step.
func(a *Allocator)resumeWorkspaceReclaim(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize reclaimAuthority)(workspaceReclaimResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.resumeWorkspaceReclaimLocked(ctx,grant,g,naming,extras,authorize,true)
}
func(a *Allocator)observeWorkspaceReclaim(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize reclaimAuthority)(workspaceReclaimResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.resumeWorkspaceReclaimLocked(ctx,grant,g,naming,extras,authorize,false)
}
func(a *Allocator)resumeWorkspaceReclaimLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize reclaimAuthority,write bool)(workspaceReclaimResult,error){
	var zero workspaceReclaimResult
	if authorize==nil{return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	m,raw,err:=readWorkspaceReclaimAttempt(p,r,e)
	if err!=nil{return zero,err}
	op,err:=openDir(a.root,grant.Request.OperationID)
	if err!=nil{return zero,err};defer op.Close()
	w,err:=openDir(op,grant.TransferID)
	if err!=nil{return zero,err};defer w.Close()
	media,err:=openDir(w,"media")
	if err!=nil{return zero,err};defer media.Close()
	recheck:=func()(int,error){
		witness,err:=a.observeTerminalReceiptLocked(ctx,grant,g,naming,extras)
		if err!=nil||witness!=m.Witness{return 0,ErrStageRecovery}
		if err:=authorize(ctx,grant,witness);err!=nil{return 0,ErrStageRefused}
		again,err:=a.observeTerminalReceiptLocked(ctx,grant,g,naming,extras)
		if err!=nil||again!=witness{return 0,ErrStageRecovery}
		if !a.livePublicationRoots(grant,r,p,src,dst)||ctx.Err()!=nil{return 0,ErrStageRefused}
		for _,v:=range []struct{path string;root *os.Root}{
			{filepath.Join(a.path,grant.Request.OperationID,grant.TransferID),w},
			{filepath.Join(a.path,grant.Request.OperationID,grant.TransferID,"media"),media},
		}{
			_,current,err:=directory(v.path)
			held,e:=v.root.Stat(".")
			if err!=nil||e!=nil||!os.SameFile(current,held){return 0,ErrStageRecovery}
		}
		return workspaceProgress(ctx,w,media,m,true)
	}
	progress,err:=recheck()
	if err!=nil{return zero,err}
	if _,err:=p.Lstat("workspace-reclaim-result.json");err==nil{
		if progress!=2{return zero,ErrStageRecovery}
		return readWorkspaceReclaimResult(p,m,raw)
	}else if !errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	if !write{return zero,ErrStageRecovery}
	for i:=progress;i<2;i++{
		current,err:=recheck()
		if err!=nil||current!=i{return zero,ErrStageRecovery}
		// Rooted, exact file only. No RemoveAll, directory deletion,
		// gallery unlink, or unlink of a previously absent entry.
		if err:=media.Remove(m.Files[i].Name);err!=nil{return zero,ErrStageRecovery}
		syncDirectory(media)
	}
	progress,err=recheck()
	if err!=nil||progress!=2{return zero,ErrStageRecovery}
	want:=expectedWorkspaceReclaimResult(m,raw)
	body,err:=json.Marshal(want)
	if err!=nil{return zero,ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"workspace-reclaim-result.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	return a.resumeWorkspaceReclaimLocked(ctx,grant,g,naming,extras,authorize,false)
}
