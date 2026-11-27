package acquisitionstage

import(
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// PolicyObserver is a trusted local owner seam. The expected digest
// MUST come from the exact preflight approval independently bound to
// this allocation, never from an untrusted requester/body. This is
// not production cross-process authentication or import authorization.
type PolicyObserver func(context.Context,workerproto.StageAllocation)([]byte,string,error)

type policyBinding struct{
	Version int `json:"version"`
	Allocation workerproto.StageAllocation `json:"allocation"`
	SealSHA256 string `json:"seal_sha256"`
	ProjectionSHA256 string `json:"projection_sha256"`
}

func sealDigest(e SealedEvidence)(string,error){
	body,err:=json.Marshal(e)
	if err!=nil||len(body)>maxSealRecordBytes{return "",ErrStageRecovery}
	return digestBytes(body),nil
}
// readPolicyBinding validates bounded structural state only; actual
// artifact hash and semantic membership checked on sealed access.
func readPolicyBinding(sealed *os.Root,e SealedEvidence)(policyBinding,error){
	var zero policyBinding
	p,err:=openDir(sealed,"policy")
	if err!=nil{return zero,err}
	defer p.Close()
	entries,err:=list(p,2)
	if err!=nil||len(entries)!=2{return zero,ErrStageRecovery}
	raw,err:=boundedRegularBytes(p,"binding.json",maxRecordBytes)
	if err!=nil{return zero,err}
	var b policyBinding
	if json.Unmarshal(raw,&b)!=nil||b.Version!=1||b.Allocation!=e.Allocation||
		!validDigest(b.ProjectionSHA256){return zero,ErrStageRecovery}
	canonical,err:=json.Marshal(b)
	if err!=nil||!bytes.Equal(raw,canonical){return zero,ErrStageRecovery}
	sealHash,err:=sealDigest(e)
	if err!=nil||sealHash!=b.SealSHA256{return zero,ErrStageRecovery}
	info,err:=p.Lstat("projection.json")
	if err!=nil||!info.Mode().IsRegular()||info.Size()<1||info.Size()>acquisitionplan.MaxPolicyProjectionBytes{return zero,ErrStageRecovery}
	return b,nil
}
func verifyBoundPolicy(sealed *os.Root,e SealedEvidence,manifest []byte)(acquisitionplan.ApprovedPolicySnapshot,error){
	var zero acquisitionplan.ApprovedPolicySnapshot
	b,err:=readPolicyBinding(sealed,e)
	if err!=nil{return zero,err}
	p,err:=openDir(sealed,"policy")
	if err!=nil{return zero,err}
	defer p.Close()
	body,err:=boundedRegularBytes(p,"projection.json",acquisitionplan.MaxPolicyProjectionBytes)
	if err!=nil{return zero,err}
	snapshot,err:=acquisitionplan.DecodeApprovedPolicy(body,e.Allocation.Request.Transfer,b.ProjectionSHA256,manifest)
	if err!=nil{return zero,ErrStageRecovery}
	approved:=make(map[string]bool,len(snapshot.Rows))
	for _,r:=range snapshot.Rows{approved[r.PostID]=true}
	for _,f:=range e.Files{if !approved[f.ID]{return zero,ErrStageRecovery}}
	return snapshot,nil
}

// BindPolicy stores one immutable mapped-policy artifact before any
// publication attempt. A partial binding refuses replay rather than
// reconstructing a new policy. Existing diagnostic publication is not
// a production dispatcher; eventual import must require this binding.
func(a *Allocator)BindPolicy(ctx context.Context,g workerproto.StageAllocation,observe PolicyObserver)error{
	a.mu.Lock();defer a.mu.Unlock()
	if observe==nil{return ErrStageRefused}
	e,err:=a.sealedLocked(ctx,g)
	if err!=nil{return err}
	s,err:=a.scope(ctx,g.Request)
	if err!=nil{return err}
	r,err:=a.load(g.Request.OperationID)
	if err!=nil||r.Allocation!=g||!matches(r,s){return ErrStageRefused}
	op,err:=openDir(a.root,g.Request.OperationID)
	if err!=nil{return err};defer op.Close()
	if _,err:=op.Lstat("publication");!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	sealed,err:=openDir(op,"sealed")
	if err!=nil{return err};defer sealed.Close()
	if _,err:=sealed.Lstat("policy");!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	manifest,err:=boundedRegularBytes(sealed,"manifest.json",workerproto.MaxMetadataOutputBytes)
	if err!=nil{return err}
	body,digest,err:=observe(ctx,g)
	if err!=nil||ctx.Err()!=nil||len(body)>acquisitionplan.MaxPolicyProjectionBytes{return ErrStageRefused}
	body=bytes.Clone(body)
	snapshot,err:=acquisitionplan.DecodeApprovedPolicy(body,g.Request.Transfer,digest,manifest)
	if err!=nil{return ErrStageRefused}
	approved:=make(map[string]bool,len(snapshot.Rows))
	for _,r:=range snapshot.Rows{approved[r.PostID]=true}
	for _,f:=range e.Files{if !approved[f.ID]{return ErrStageRefused}}
	free,err:=a.freeBytes(a.path)
	if err!=nil||free<uint64(len(body))+maxRecordBytes{return ErrStageLimit}
	s,err=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,s){return ErrStageRefused}
	hash,err:=sealDigest(e)
	if err!=nil{return err}
	b:=policyBinding{Version:1,Allocation:g,SealSHA256:hash,ProjectionSHA256:digest}
	raw,err:=json.Marshal(b)
	if err!=nil||len(raw)>maxRecordBytes{return ErrStageRecovery}
	if err:=sealed.Mkdir("policy",0700);err!=nil{return ErrStageRecovery}
	p,err:=openDir(sealed,"policy")
	if err!=nil{return err};defer p.Close()
	if err:=writeOwnerBytes(ctx,p,"projection.json",body);err!=nil{return err}
	if err:=writeOwnerBytes(ctx,p,"binding.json",raw);err!=nil{return err}
	syncDirectory(p);syncDirectory(sealed);syncDirectory(op);syncDirectory(a.root)
	s,err=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,s){return ErrStageRefused}
	if _,err:=verifyBoundPolicy(sealed,e,manifest);err!=nil{return err}
	return nil
}

// BoundPolicy rechecks current scope, sealed bytes and the exact durable
// binding. Does not authorize publication or database mutation.
func(a *Allocator)BoundPolicy(ctx context.Context,g workerproto.StageAllocation)(acquisitionplan.ApprovedPolicySnapshot,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.boundPolicyLocked(ctx,g)
}
func(a *Allocator)boundPolicyLocked(ctx context.Context,g workerproto.StageAllocation)(acquisitionplan.ApprovedPolicySnapshot,error){
	var zero acquisitionplan.ApprovedPolicySnapshot
	e,err:=a.sealedLocked(ctx,g)
	if err!=nil{return zero,err}
	s,err:=a.scope(ctx,g.Request)
	if err!=nil{return zero,err}
	r,err:=a.load(g.Request.OperationID)
	if err!=nil||r.Allocation!=g||!matches(r,s){return zero,ErrStageRefused}
	op,err:=openDir(a.root,g.Request.OperationID)
	if err!=nil{return zero,err};defer op.Close()
	sealed,err:=openDir(op,"sealed")
	if err!=nil{return zero,err};defer sealed.Close()
	manifest,err:=boundedRegularBytes(sealed,"manifest.json",workerproto.MaxMetadataOutputBytes)
	if err!=nil{return zero,err}
	snapshot,err:=verifyBoundPolicy(sealed,e,manifest)
	if err!=nil{return zero,err}
	s,err=a.scope(ctx,g.Request)
	if err!=nil||!matches(r,s){return zero,ErrStageRefused}
	return snapshot,nil
}
