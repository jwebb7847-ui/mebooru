package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// AbandonAuthority must prove no live or future writer for this exact
// allocation, not merely that a cancellation request was sent.
type AbandonAuthority func(context.Context,workerproto.StageAllocation)error

type AbandonedOperation struct{
	Version int
	Allocation workerproto.StageAllocation
	WorkspaceIdentity string
	Sealed bool
	RetainedReservation uint64
}

const maxAbandonRecord=64<<10

func workspaceEmpty(root *os.Root)(bool,error){
	f,err:=root.Open(".")
	if err!=nil{return false,ErrStageRecovery}
	defer f.Close()
	entries,err:=f.ReadDir(1)
	if err!=nil&&err!=io.EOF{return false,ErrStageRecovery}
	return len(entries)==0,nil
}

func abandonState(op *os.Root,r record,marked bool)(AbandonedOperation,error){
	var zero AbandonedOperation
	if _,err:=op.Lstat("publication");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRefused}
	entries,err:=list(op,4)
	if err!=nil{return zero,err}
	sealed:=false
	for _,v:=range entries{
		switch v.Name(){
		case "allocation.json",r.Allocation.TransferID:
		case "sealed":sealed=true
		case "abandoned":if !marked{return zero,ErrStageRecovery}
		default:return zero,ErrStageRecovery
		}
	}
	want:=2
	if sealed{want++;if _,err:=readSeal(op,r.Allocation);err!=nil{return zero,err}}
	if marked{want++}
	if len(entries)!=want{return zero,ErrStageRecovery}
	w,err:=openDir(op,r.Allocation.TransferID)
	if err!=nil{return zero,err}
	defer w.Close()
	identity,err:=rootIdentity(w)
	if err!=nil{return zero,err}
	empty,err:=workspaceEmpty(w)
	if err!=nil{return zero,err}
	charge:=r.Allocation.MaxBytes
	if empty&&!sealed{charge=0}
	return AbandonedOperation{Version:1,Allocation:r.Allocation,WorkspaceIdentity:identity,Sealed:sealed,RetainedReservation:charge},nil
}

func readAbandoned(op *os.Root,r record)(AbandonedOperation,error){
	var zero AbandonedOperation
	d,err:=openDir(op,"abandoned")
	if err!=nil{return zero,err}
	defer d.Close()
	entries,err:=list(d,1)
	if err!=nil||len(entries)!=1||entries[0].Name()!="record.json"{return zero,ErrStageRecovery}
	raw,err:=boundedRegularBytes(d,"record.json",maxAbandonRecord)
	if err!=nil{return zero,err}
	want,err:=abandonState(op,r,true)
	if err!=nil{return zero,err}
	body,err:=json.Marshal(want)
	if err!=nil||!bytes.Equal(raw,body){return zero,ErrStageRecovery}
	return want,nil
}

// AbandonPrepublication permanently refuses further allocation/workspace
// or sealing for a cancelled prepublication operation. No payload or audit
// evidence is deleted. Empty allocations release their reservation;
// nonempty/sealed quarantines retain the full byte reservation until a
// future explicit reclamation policy. No uncertain publication is eligible.
func(a *Allocator) AbandonPrepublication(ctx context.Context,q workerproto.StageAbandonRequest,authorize AbandonAuthority)(AbandonedOperation,error){
	a.mu.Lock();defer a.mu.Unlock()
	var zero AbandonedOperation
	if !workerproto.ValidStageAbandon(q)||authorize==nil||ctx.Err()!=nil||!a.live(){return zero,ErrStageRefused}
	r,err:=a.load(q.OperationID)
	if err!=nil||r.Allocation.TransferID!=q.TransferID{return zero,ErrStageRefused}
	s,err:=a.scope(ctx,r.Allocation.Request)
	if err!=nil||!matches(r,s)||authorize(ctx,r.Allocation)!=nil{return zero,ErrStageRefused}
	op,err:=openDir(a.root,q.OperationID)
	if err!=nil{return zero,err}
	defer op.Close()
	if _,err:=op.Lstat("abandoned");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRecovery}
	m,err:=abandonState(op,r,false)
	if err!=nil{return zero,err}
	body,err:=json.Marshal(m)
	if err!=nil||len(body)>maxAbandonRecord{return zero,ErrStageLimit}
	s,err=a.scope(ctx,r.Allocation.Request)
	if err!=nil||!matches(r,s)||authorize(ctx,r.Allocation)!=nil||!a.live(){return zero,ErrStageRefused}
	again,err:=abandonState(op,r,false)
	if err!=nil||again!=m{return zero,ErrStageRecovery}
	if err:=op.Mkdir("abandoned",0700);err!=nil{return zero,ErrStageRecovery}
	syncDirectory(op)
	d,err:=openDir(op,"abandoned")
	if err!=nil{return zero,err}
	defer d.Close()
	if err:=writeOwnerBytes(ctx,d,"record.json",body);err!=nil{return zero,err}
	syncDirectory(d)
	observed,err:=readAbandoned(op,r)
	if err!=nil||observed!=m||authorize(ctx,r.Allocation)!=nil{return zero,ErrStageRecovery}
	return observed,nil
}
