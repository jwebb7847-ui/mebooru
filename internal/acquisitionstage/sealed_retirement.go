package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// Last media-payload unlink. Separate current owner decision, never
// implied by prior cleanup. Preserve native sidecar and every audit
// record, reserve the operation identity and retain bounded evidence.
type sealedRetirementAuthority func(context.Context,workerproto.StageAllocation,terminalReceiptWitness)error
const maxSealedRetirementRecord = 256 << 10
type sealedRetirementAttempt struct{
	Version int `json:"version"`
	Witness terminalReceiptWitness `json:"witness"`
	PreparedResultSHA256 string `json:"prepared_result_sha256"`
	SealSHA256 string `json:"seal_sha256"`
	MediaIdentity string `json:"media_identity"`
	Payload workspaceFile `json:"payload"`
	Sidecar workspaceFile `json:"sidecar"`
}
type sealedRetirementResult struct{
	Version int `json:"version"`
	AttemptSHA256 string `json:"attempt_sha256"`
	RemovedBytes int64 `json:"removed_bytes"`
	RetainedSidecarBytes int64 `json:"retained_sidecar_bytes"`
}
func readSealedRetirementAttempt(p *os.Root,r record,e SealedEvidence)(sealedRetirementAttempt,[]byte,error){
	var zero sealedRetirementAttempt
	raw,err:=boundedRegularBytes(p,"sealed-retirement-attempt.json",maxSealedRetirementRecord)
	if err!=nil{return zero,nil,err}
	var m sealedRetirementAttempt
	if json.Unmarshal(raw,&m)!=nil||m.Version!=1||len(e.Files)!=1||m.MediaIdentity==""||len(m.MediaIdentity)>128{return zero,nil,ErrStageRecovery}
	pr,praw,err:=readPreparedReleaseAttempt(p,r,e)
	if err!=nil||pr.Witness!=m.Witness{return zero,nil,ErrStageRecovery}
	result,err:=readPreparedReleaseResult(p,pr,praw)
	if err!=nil{return zero,nil,err}
	body,err:=json.Marshal(result)
	if err!=nil||digestBytes(body)!=m.PreparedResultSHA256{return zero,nil,ErrStageRecovery}
	sh,err:=sealDigest(e)
	if err!=nil||sh!=m.SealSHA256{return zero,nil,ErrStageRecovery}
	f:=e.Files[0]
	if !validWorkspaceFile(m.Payload,f.RelativeName,f.Size,f.SHA256)||!validWorkspaceFile(m.Sidecar,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256)||
		m.Payload.Identity==m.Sidecar.Identity||m.Payload.Identity==m.Witness.FileIdentity||m.Payload.Identity==pr.Prepared.Identity||
		m.MediaIdentity==m.Payload.Identity||m.MediaIdentity==m.Sidecar.Identity||m.MediaIdentity==pr.MediaIdentity{return zero,nil,ErrStageRecovery}
	w,_,err:=readWorkspaceReclaimAttempt(p,r,e)
	if err!=nil{return zero,nil,err}
	seen:=map[string]bool{m.Witness.FileIdentity:true,pr.Prepared.Identity:true,pr.MediaIdentity:true,w.WorkspaceIdentity:true,w.MediaIdentity:true}
	for _,v:=range []workspaceFile{w.Files[0],w.Files[1],w.Manifest,w.Probe}{seen[v.Identity]=true}
	if seen[m.MediaIdentity]||seen[m.Payload.Identity]||seen[m.Sidecar.Identity]{return zero,nil,ErrStageRecovery}
	canonical,err:=json.Marshal(m)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,nil,ErrStageRecovery}
	return m,raw,nil
}
func expectedSealedRetirement(m sealedRetirementAttempt,raw []byte)sealedRetirementResult{
	return sealedRetirementResult{Version:1,AttemptSHA256:digestBytes(raw),RemovedBytes:m.Payload.Size,RetainedSidecarBytes:m.Sidecar.Size}
}
func readSealedRetirementResult(p *os.Root,m sealedRetirementAttempt,raw []byte)(sealedRetirementResult,error){
	var zero sealedRetirementResult
	want:=expectedSealedRetirement(m,raw)
	body,err:=boundedRegularBytes(p,"sealed-retirement-result.json",maxSealedRetirementRecord)
	canonical,e:=json.Marshal(want)
	if err!=nil||e!=nil||!bytes.Equal(body,canonical){return zero,ErrStageRecovery}
	return want,nil
}
func verifySealedRetirementInventory(ctx context.Context,media *os.Root,m sealedRetirementAttempt,retired,verifyBytes bool)error{
	id,err:=rootIdentity(media)
	if err!=nil||id!=m.MediaIdentity{return ErrStageRecovery}
	items,err:=list(media,2)
	if err!=nil{return err}
	n:=2;if retired{n=1}
	if len(items)!=n{return ErrStageRecovery}
	for i,v:=range []workspaceFile{m.Payload,m.Sidecar}{
		if i==0&&retired{
			if _,err:=media.Lstat(v.Name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
			continue
		}
		if verifyBytes{
			got,err:=workspaceFileFor(ctx,media,v.Name,v.Size,v.SHA256)
			if err!=nil||got!=v{return ErrStageRecovery}
		}else{
			info,err:=media.Lstat(v.Name)
			if err!=nil||!info.Mode().IsRegular()||info.Size()!=v.Size{return ErrStageRecovery}
			file,err:=media.Open(v.Name)
			if err!=nil{return ErrStageRecovery}
			actual,err:=file.Stat()
			if err!=nil||!os.SameFile(info,actual)||!singleLink(file,actual){file.Close();return ErrStageRecovery}
			id,ok:=diskFileIdentity(file,actual);file.Close()
			if !ok||id!=v.Identity{return ErrStageRecovery}
		}
	}
	return nil
}
// Structural inventory recognizes only the exact current retirement
// attempt. Normal Sealed/BoundPolicy still require all media bytes.
func checkSealedInventory(op,media *os.Root,grant workerproto.StageAllocation,e SealedEvidence)error{
	p,err:=openDir(op,"publication")
	if err!=nil{
		if _,err:=op.Lstat("publication");!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
		if !exactInventory(media,e.Files){return ErrStageRecovery};return nil
	}
	defer p.Close()
	if _,err:=p.Lstat("sealed-retirement-attempt.json");errors.Is(err,os.ErrNotExist){
		if _,err:=p.Lstat("sealed-retirement-result.json");!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
		if !exactInventory(media,e.Files){return ErrStageRecovery};return nil
	}else if err!=nil{return ErrStageRecovery}
	r,err:=readRecord(op)
	if err!=nil||r.Allocation!=grant{return ErrStageRecovery}
	m,raw,err:=readSealedRetirementAttempt(p,r,e)
	if err!=nil{return err}
	_,err=media.Lstat(m.Payload.Name)
	retired:=errors.Is(err,os.ErrNotExist)
	if err!=nil&&!retired{return ErrStageRecovery}
	if err:=verifySealedRetirementInventory(context.Background(),media,m,retired,false);err!=nil{return err}
	if _,err:=p.Lstat("sealed-retirement-result.json");err==nil{
		if !retired{return ErrStageRecovery}
		_,err=readSealedRetirementResult(p,m,raw)
		return err
	}else if !errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	return nil
}
func checkRetiredSeal(op,media *os.Root,r record,e SealedEvidence)error{
	p,err:=openDir(op,"publication")
	if err!=nil{return err};defer p.Close()
	m,_,err:=readSealedRetirementAttempt(p,r,e)
	if err!=nil{return err}
	return verifySealedRetirementInventory(context.Background(),media,m,true,false)
}
func(a *Allocator)beginSealedRetirementLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize sealedRetirementAuthority)error{
	if authorize==nil{return ErrStageRefused}
	w,err:=a.observeTerminalReceiptModeLocked(ctx,grant,g,naming,extras,true)
	if err!=nil{return err}
	if err:=authorize(ctx,grant,w);err!=nil{return ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err};defer p.Close();defer src.Close();defer dst.Close()
	for _,name:=range []string{"sealed-retirement-attempt.json","sealed-retirement-result.json"}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	pr,praw,err:=readPreparedReleaseAttempt(p,r,e)
	if err!=nil||pr.Witness!=w{return ErrStageRecovery}
	result,err:=readPreparedReleaseResult(p,pr,praw)
	if err!=nil{return err}
	body,err:=json.Marshal(result)
	if err!=nil{return ErrStageRecovery}
	m:=sealedRetirementAttempt{Version:1,Witness:w,PreparedResultSHA256:digestBytes(body)}
	m.SealSHA256,err=sealDigest(e)
	if err!=nil||len(e.Files)!=1{return ErrStageRecovery}
	op,err:=openDir(a.root,grant.Request.OperationID)
	if err!=nil{return err};defer op.Close()
	sealed,err:=openDir(op,"sealed")
	if err!=nil{return err};defer sealed.Close()
	media,err:=openDir(sealed,"media")
	if err!=nil{return err};defer media.Close()
	f:=e.Files[0]
	m.MediaIdentity,err=rootIdentity(media)
	if err!=nil{return err}
	m.Payload,err=workspaceFileFor(ctx,media,f.RelativeName,f.Size,f.SHA256)
	if err!=nil{return err}
	m.Sidecar,err=workspaceFileFor(ctx,media,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256)
	if err!=nil{return err}
	if err:=verifySealedRetirementInventory(ctx,media,m,false,true);err!=nil{return err}
	again,err:=a.observeTerminalReceiptModeLocked(ctx,grant,g,naming,extras,true)
	if err!=nil||again!=w{return ErrStageRecovery}
	if err:=authorize(ctx,grant,w);err!=nil{return ErrStageRefused}
	again,err=a.observeTerminalReceiptModeLocked(ctx,grant,g,naming,extras,true)
	if err!=nil||again!=w||!a.livePublicationRoots(grant,r,p,src,dst)||!a.liveSealedMedia(grant,media){return ErrStageRecovery}
	if err:=verifySealedRetirementInventory(ctx,media,m,false,true);err!=nil{return err}
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxSealedRetirementRecord{return ErrStageLimit}
	if err:=writeOwnerBytes(ctx,p,"sealed-retirement-attempt.json",raw);err!=nil{return err}
	syncDirectory(p)
	return nil
}
func(a *Allocator)liveSealedMedia(grant workerproto.StageAllocation,media *os.Root)bool{
	_,current,err:=directory(filepath.Join(a.path,grant.Request.OperationID,"sealed","media"))
	held,e:=media.Stat(".")
	return a.live()&&err==nil&&e==nil&&os.SameFile(current,held)
}
func(a *Allocator)retireSealedMedia(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize sealedRetirementAuthority)(sealedRetirementResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	if err:=a.beginSealedRetirementLocked(ctx,grant,g,naming,extras,authorize);err!=nil{return sealedRetirementResult{},err}
	return a.resumeSealedRetirementLocked(ctx,grant,g,naming,extras,authorize,true)
}
func(a *Allocator)resumeSealedRetirement(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize sealedRetirementAuthority)(sealedRetirementResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.resumeSealedRetirementLocked(ctx,grant,g,naming,extras,authorize,true)
}
func(a *Allocator)observeSealedRetirement(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize sealedRetirementAuthority)(sealedRetirementResult,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.resumeSealedRetirementLocked(ctx,grant,g,naming,extras,authorize,false)
}
func(a *Allocator)resumeSealedRetirementLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,authorize sealedRetirementAuthority,write bool)(sealedRetirementResult,error){
	var zero sealedRetirementResult
	if authorize==nil||!workerproto.ValidStageAllocation(grant)||!a.live(){return zero,ErrStageRefused}
	op,err:=openDir(a.root,grant.Request.OperationID)
	if err!=nil{return zero,err};defer op.Close()
	r,err:=readRecord(op)
	if err!=nil||r.Allocation!=grant||r.StageRoot!=a.path{return zero,ErrStageRecovery}
	e,err:=readSeal(op,grant)
	if err!=nil{return zero,err}
	p,err:=openDir(op,"publication")
	if err!=nil{return zero,err};defer p.Close()
	m,raw,err:=readSealedRetirementAttempt(p,r,e)
	if err!=nil{return zero,err}
	sealed,err:=openDir(op,"sealed")
	if err!=nil{return zero,err};defer sealed.Close()
	media,err:=openDir(sealed,"media")
	if err!=nil{return zero,err};defer media.Close()
	recheck:=func()(bool,error){
		_,err:=media.Lstat(m.Payload.Name)
		retired:=errors.Is(err,os.ErrNotExist)
		if err!=nil&&!retired{return false,ErrStageRecovery}
		if err:=verifySealedRetirementInventory(ctx,media,m,retired,true);err!=nil{return false,err}
		w,err:=a.observeTerminalReceiptPhaseLocked(ctx,grant,g,naming,extras,true,retired)
		if err!=nil||w!=m.Witness{return false,ErrStageRecovery}
		if err:=authorize(ctx,grant,w);err!=nil{return false,ErrStageRefused}
		again,err:=a.observeTerminalReceiptPhaseLocked(ctx,grant,g,naming,extras,true,retired)
		if err!=nil||again!=w||ctx.Err()!=nil||!a.liveSealedMedia(grant,media){return false,ErrStageRecovery}
		_,current,err:=directory(filepath.Join(a.path,grant.Request.OperationID,"publication"))
		held,e:=p.Stat(".")
		if err!=nil||e!=nil||!os.SameFile(current,held){return false,ErrStageRecovery}
		if err:=verifySealedRetirementInventory(ctx,media,m,retired,true);err!=nil{return false,err}
		return retired,nil
	}
	retired,err:=recheck()
	if err!=nil{return zero,err}
	if _,err:=p.Lstat("sealed-retirement-result.json");err==nil{
		if !retired{return zero,ErrStageRecovery}
		return readSealedRetirementResult(p,m,raw)
	}else if !errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	if !write{return zero,ErrStageRecovery}
	if !retired{
		if err:=media.Remove(m.Payload.Name);err!=nil{return zero,ErrStageRecovery}
		syncDirectory(media)
	}
	retired,err=recheck()
	if err!=nil||!retired{return zero,ErrStageRecovery}
	want:=expectedSealedRetirement(m,raw)
	body,err:=json.Marshal(want)
	if err!=nil{return zero,ErrStageRecovery}
	if err:=writeOwnerBytes(ctx,p,"sealed-retirement-result.json",body);err!=nil{return zero,err}
	syncDirectory(p)
	return a.resumeSealedRetirementLocked(ctx,grant,g,naming,extras,authorize,false)
}
