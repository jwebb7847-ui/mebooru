package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// A distinct current owner/custody decision for releasing the prepared
// file only. It must cover gallery lifetime, scan exclusion and the
// exact operation. Workspace cleanup authority is not implicitly reused.
type preparedReleaseAuthority func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error
const maxPreparedReleaseRecord = 256 << 10
type preparedReleaseAttempt struct{
	Version int `json:"version"`
	Witness terminalReceiptWitness `json:"witness"`
	WorkspaceResultSHA256 string `json:"workspace_result_sha256"`
	MediaIdentity string `json:"media_identity"`
	Prepared workspaceFile `json:"prepared"`
}
type preparedReleaseResult struct{
	Version int `json:"version"`
	AttemptSHA256 string `json:"attempt_sha256"`
	ReleasedEntryBytes int64 `json:"released_entry_bytes"`
	SharedCanonical bool `json:"shared_canonical"`
}
func preparedLinks(w terminalReceiptWitness)uint64{
	if w.Kind==heldReceiptKind{return 1}
	return 2
}
func readPreparedReleaseAttempt(p *os.Root,r record,e SealedEvidence)(preparedReleaseAttempt,[]byte,error){
	var zero preparedReleaseAttempt
	raw,err:=boundedRegularBytes(p,"prepared-release-attempt.json",maxPreparedReleaseRecord)
	if err!=nil{return zero,nil,err}
	var m preparedReleaseAttempt
	if json.Unmarshal(raw,&m)!=nil||m.Version!=1||len(e.Files)!=1||m.MediaIdentity==""||len(m.MediaIdentity)>128{return zero,nil,ErrStageRecovery}
	w,wb,err:=readWorkspaceReclaimAttempt(p,r,e)
	if err!=nil||w.Witness!=m.Witness{return zero,nil,ErrStageRecovery}
	result,err:=readWorkspaceReclaimResult(p,w,wb)
	if err!=nil{return zero,nil,err}
	body,err:=json.Marshal(result)
	if err!=nil||digestBytes(body)!=m.WorkspaceResultSHA256{return zero,nil,ErrStageRecovery}
	f:=e.Files[0]
	if !validWorkspaceFile(m.Prepared,f.RelativeName,f.Size,f.SHA256)||m.Prepared.Identity==m.MediaIdentity||
		m.MediaIdentity==m.Witness.FileIdentity||m.MediaIdentity==w.WorkspaceIdentity||m.MediaIdentity==w.MediaIdentity||
		(m.Prepared.Identity==m.Witness.FileIdentity)!=(preparedLinks(m.Witness)==2){return zero,nil,ErrStageRecovery}
	for _,v:=range []workspaceFile{w.Files[0],w.Files[1],w.Manifest,w.Probe}{
		if m.Prepared.Identity==v.Identity{return zero,nil,ErrStageRecovery}
	}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,nil,ErrStageRecovery}
	return m,raw,nil
}
func preparedReleaseExpected(m preparedReleaseAttempt,raw []byte)preparedReleaseResult{
	return preparedReleaseResult{Version:1,AttemptSHA256:digestBytes(raw),ReleasedEntryBytes:m.Prepared.Size,SharedCanonical:preparedLinks(m.Witness)==2}
}
func readPreparedReleaseResult(p *os.Root,m preparedReleaseAttempt,raw []byte)(preparedReleaseResult,error){
	var zero preparedReleaseResult
	want:=preparedReleaseExpected(m,raw)
	body,err:=boundedRegularBytes(p,"prepared-release-result.json",maxPreparedReleaseRecord)
	canonical,e:=json.Marshal(want)
	if err!=nil||e!=nil||!bytes.Equal(body,canonical){return zero,ErrStageRecovery}
	return want,nil
}
// The entry count and exact rooted directory identity are required
// both before and after release. No implicit missing-file fallback.
func verifyPreparedReleaseInventory(src *os.Root,m preparedReleaseAttempt,released bool)error{
	id,err:=rootIdentity(src)
	if err!=nil||id!=m.MediaIdentity{return ErrStageRecovery}
	items,err:=list(src,1)
	if err!=nil{return err}
	if released{
		if len(items)!=0{return ErrStageRecovery}
		if _,err:=src.Lstat(m.Prepared.Name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
		return nil
	}
	if len(items)!=1||items[0].Name()!=m.Prepared.Name||!items[0].Type().IsRegular(){return ErrStageRecovery}
	f,err:=src.Open(m.Prepared.Name)
	if err!=nil{return ErrStageRecovery}
	defer f.Close()
	info,err:=f.Stat()
	current,e:=src.Lstat(m.Prepared.Name)
	if err!=nil||e!=nil||!info.Mode().IsRegular()||!os.SameFile(info,current)||info.Size()!=m.Prepared.Size||!exactLinks(f,info,preparedLinks(m.Witness)){return ErrStageRecovery}
	id,ok:=diskFileIdentity(f,info)
	if !ok||id!=m.Prepared.Identity{return ErrStageRecovery}
	return nil
}
func validatePreparedReleaseRecords(p,src *os.Root,r record,e SealedEvidence,hasResult bool)error{
	m,raw,err:=readPreparedReleaseAttempt(p,r,e)
	if err!=nil{return err}
	_,err=src.Lstat(m.Prepared.Name)
	released:=errors.Is(err,os.ErrNotExist)
	if err!=nil&&!released{return ErrStageRecovery}
	if err:=verifyPreparedReleaseInventory(src,m,released);err!=nil{return err}
	if hasResult{
		if !released{return ErrStageRecovery}
		_,err:=readPreparedReleaseResult(p,m,raw)
		return err
	}
	return nil
}
func verifyReleasedNamingFiles(ctx context.Context,src,dst,parent *os.Root,f acquisitionplan.VerifiedTransferFile,m namingAttempt)error{
	if _,err:=src.Lstat(f.RelativeName);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	if _,err:=dst.Lstat(m.Old);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	id,err:=namingFile(ctx,parent,filepath.Base(m.Final),f,1)
	if err!=nil||id!=m.FileIdentity{return ErrStageRecovery}
	return nil
}
func verifyCompletedHeldPlacement(ctx context.Context,grant workerproto.StageAllocation,e SealedEvidence,p,src,dst *os.Root,released bool)error{
	if !released{
		places,err:=inspectPlacements(ctx,grant,e,p,src,dst)
		if err!=nil||len(places)!=1||places[0].State!="absent"{return ErrStageRecovery}
		return nil
	}
	if len(e.Files)!=1||ctx.Err()!=nil{return ErrStageRecovery}
	f:=e.Files[0]
	if _,err:=src.Lstat(f.RelativeName);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	if _,err:=dst.Lstat(finalName(grant,f));!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	// The calling held observer additionally verifies the independent
	// original canonical, every retained alias and exact database state.
	return nil
}
func(a *Allocator)beginPreparedReleaseLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize preparedReleaseAuthority)error{
	if authorize==nil{return ErrStageRefused}
	witness,err:=a.observeTerminalReceiptLocked(ctx,grant,g,naming,extras)
	if err!=nil{return err}
	if err:=authorize(ctx,grant,witness);err!=nil{return ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err};defer p.Close();defer src.Close();defer dst.Close()
	for _,name:=range []string{"prepared-release-attempt.json","prepared-release-result.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	w,raw,err:=readWorkspaceReclaimAttempt(p,r,e)
	if err!=nil||w.Witness!=witness{return ErrStageRecovery}
	wr,err:=readWorkspaceReclaimResult(p,w,raw)
	if err!=nil{return err}
	wrBody,err:=json.Marshal(wr)
	if err!=nil{return ErrStageRecovery}
	if len(e.Files)!=1{return ErrStageRefused}
	f:=e.Files[0]
	m:=preparedReleaseAttempt{Version:1,Witness:witness,WorkspaceResultSHA256:digestBytes(wrBody)}
	m.MediaIdentity,err=rootIdentity(src)
	if err!=nil{return err}
	id,err:=namingFile(ctx,src,f.RelativeName,f,preparedLinks(witness))
	if err!=nil{return err}
	m.Prepared=workspaceFile{Name:f.RelativeName,Size:f.Size,SHA256:f.SHA256,Identity:id}
	if (id==witness.FileIdentity)!=(preparedLinks(witness)==2){return ErrStageRecovery}
	if err:=verifyPreparedReleaseInventory(src,m,false);err!=nil{return err}
	again,err:=a.observeTerminalReceiptLocked(ctx,grant,g,naming,extras)
	if err!=nil||again!=witness{return ErrStageRecovery}
	if err:=authorize(ctx,grant,witness);err!=nil{return ErrStageRefused}
	again,err=a.observeTerminalReceiptLocked(ctx,grant,g,naming,extras)
	if err!=nil||again!=witness||!a.livePublicationRoots(grant,r,p,src,dst){return ErrStageRecovery}
	if err:=verifyPreparedReleaseInventory(src,m,false);err!=nil{return err}
	body,err:=json.Marshal(m)
	if err!=nil||len(body)>maxPreparedReleaseRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"prepared-release-attempt.json",body);err!=nil{return err}
	syncDirectory(p)
	return nil
}
func(a *Allocator)releasePrepared(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize preparedReleaseAuthority)(preparedReleaseResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	if err:=a.beginPreparedReleaseLocked(ctx,grant,g,naming,extras,authorize);err!=nil{return preparedReleaseResult{},err}
	return a.resumePreparedReleaseLocked(ctx,grant,g,naming,extras,authorize,true)
}
func(a *Allocator)resumePreparedRelease(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize preparedReleaseAuthority)(preparedReleaseResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.resumePreparedReleaseLocked(ctx,grant,g,naming,extras,authorize,true)
}
func(a *Allocator)observePreparedRelease(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize preparedReleaseAuthority)(preparedReleaseResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.resumePreparedReleaseLocked(ctx,grant,g,naming,extras,authorize,false)
}
func(a *Allocator)resumePreparedReleaseLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize preparedReleaseAuthority,write bool)(preparedReleaseResult,error){
	var zero preparedReleaseResult
	if authorize==nil{return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err};defer p.Close();defer src.Close();defer dst.Close()
	m,raw,err:=readPreparedReleaseAttempt(p,r,e)
	if err!=nil{return zero,err}
	recheck:=func()(bool,error){
		_,err:=src.Lstat(m.Prepared.Name)
		released:=errors.Is(err,os.ErrNotExist)
		if err!=nil&&!released{return false,ErrStageRecovery}
		if err:=verifyPreparedReleaseInventory(src,m,released);err!=nil{return false,err}
		w,err:=a.observeTerminalReceiptModeLocked(ctx,grant,g,naming,extras,released)
		if err!=nil||w!=m.Witness{return false,ErrStageRecovery}
		if err:=authorize(ctx,grant,w);err!=nil{return false,ErrStageRefused}
		again,err:=a.observeTerminalReceiptModeLocked(ctx,grant,g,naming,extras,released)
		if err!=nil||again!=w||ctx.Err()!=nil||!a.livePublicationRoots(grant,r,p,src,dst){return false,ErrStageRecovery}
		if err:=verifyPreparedReleaseInventory(src,m,released);err!=nil{return false,err}
		if !released{
			f:=e.Files[0]
			id,err:=namingFile(ctx,src,f.RelativeName,f,preparedLinks(w))
			if err!=nil||id!=m.Prepared.Identity{return false,ErrStageRecovery}
		}
		return released,nil
	}
	released,err:=recheck()
	if err!=nil{return zero,err}
	if _,err:=p.Lstat("prepared-release-result.json");err==nil{
		if !released{return zero,ErrStageRecovery}
		return readPreparedReleaseResult(p,m,raw)
	}else if !errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	if !write{return zero,ErrStageRecovery}
	if !released{
		// Exact operation-owned entry; never unlink the gallery canonical.
		if err:=src.Remove(m.Prepared.Name);err!=nil{return zero,ErrStageRecovery}
		syncDirectory(src)
	}
	released,err=recheck()
	if err!=nil||!released{return zero,ErrStageRecovery}
	want:=preparedReleaseExpected(m,raw)
	body,err:=json.Marshal(want)
	if err!=nil{return zero,ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"prepared-release-result.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	return a.resumePreparedReleaseLocked(ctx,grant,g,naming,extras,authorize,false)
}
