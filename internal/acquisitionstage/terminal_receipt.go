package acquisitionstage

import(
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// A read-only common observation of one of the three composed receipts.
// Not a reclamation grant, operation result, retry token or IPC DTO.
// The caller still needs exclusive gallery and staging lifetime and
// custody, current owner decisions and a separate durable reclaim plan.
type terminalReceiptWitness struct{
	Kind string
	Allocation workerproto.StageAllocation
	GalleryIdentity string
	ReceiptSHA256 string
	PolicySHA256 string
	Row newReceiptRow
	PathID int64
	FileIdentity string
}

func(a *Allocator)observeTerminalReceipt(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(terminalReceiptWitness,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeTerminalReceiptLocked(ctx,grant,g,naming,extras)
}
func(a *Allocator)observeTerminalReceiptLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority)(terminalReceiptWitness,error){
	return a.observeTerminalReceiptModeLocked(ctx,grant,g,naming,extras,false)
}
func(a *Allocator)observeTerminalReceiptModeLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,released bool)(terminalReceiptWitness,error){
	return a.observeTerminalReceiptPhaseLocked(ctx,grant,g,naming,extras,released,false)
}
func(a *Allocator)observeTerminalReceiptPhaseLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,naming namingAuthority,extras extrasAuthority,released,retired bool)(terminalReceiptWitness,error){
	var zero terminalReceiptWitness
	if (retired&&!released)||g==nil||g.WatcherRunning()||extras==nil{return zero,ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRootsMode(ctx,grant,retired)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	// The narrow old receipt and the three composed receipt types are
	// not interchangeable. No caller-supplied disposition or inference
	// from an absent alias, an image row or an attempt alone.
	if _,err:=p.Lstat("new-receipt.json");!errors.Is(err,os.ErrNotExist){return zero,ErrStageRefused}
	type candidate struct{name string;limit int64}
	var selected candidate
	for _,c:=range []candidate{
		{"composed-receipt.json",maxComposedReceipt},
		{"held-receipt.json",maxHeldReceipt},
		{"restored-receipt.json",maxRestoredReceipt},
	}{
		_,err:=p.Lstat(c.name)
		if errors.Is(err,os.ErrNotExist){continue}
		if err!=nil||selected.name!=""{return zero,ErrStageRecovery}
		selected=c
	}
	if selected.name==""{return zero,ErrStageRecovery}
	before,err:=boundedRegularBytes(p,selected.name,selected.limit)
	if err!=nil{return zero,err}
	l,err:=readLandingAttempt(p,r)
	if err!=nil{return zero,err}
	var out terminalReceiptWitness
	var raw []byte
	switch selected.name{
	case "composed-receipt.json":
		if !l.NewOnly||l.RestoreImageID!=0||naming==nil{return zero,ErrStageRefused}
		v,err:=a.composedReceiptModeLocked(ctx,grant,g,naming,extras,false,released,retired)
		if err!=nil{return zero,err}
		out=terminalReceiptWitness{Kind:v.Kind,Allocation:v.Allocation,GalleryIdentity:v.GalleryIdentity,PolicySHA256:v.PolicySHA256,
			Row:v.Row,PathID:v.PathID,FileIdentity:v.FileIdentity}
		raw,err=json.Marshal(v)
		if err!=nil{return zero,ErrStageRecovery}
	case "held-receipt.json":
		if l.NewOnly||l.RestoreImageID!=0{return zero,ErrStageRefused}
		v,err:=a.heldReceiptModeLocked(ctx,grant,g,extras,false,released,retired)
		if err!=nil{return zero,err}
		m,err:=readMetadataAttempt(p,l,e)
		if err!=nil{return zero,err}
		result,err:=readMetadataResult(p,m)
		if err!=nil{return zero,err}
		held,err:=readHeldComposition(p,r,e,l,result)
		if err!=nil||held.Held!=v.Held{return zero,ErrStageRecovery}
		var pathID int64
		for _,path:=range held.Retained{if path.Canonical{pathID=path.ID}}
		out=terminalReceiptWitness{Kind:v.Kind,Allocation:v.Allocation,GalleryIdentity:v.GalleryIdentity,PolicySHA256:v.PolicySHA256,
			Row:newReceiptRow{PostID:v.Held.PostID,ImageID:v.Held.ImageID,Name:v.Held.CanonicalPath,Size:v.Held.Size,SHA256:v.Held.SHA256},
			PathID:pathID,FileIdentity:v.Held.CanonicalIdentity}
		raw,err=json.Marshal(v)
		if err!=nil{return zero,ErrStageRecovery}
	case "restored-receipt.json":
		if l.NewOnly||l.RestoreImageID<=0||naming==nil{return zero,ErrStageRefused}
		v,err:=a.restoredReceiptModeLocked(ctx,grant,g,naming,extras,false,released,retired)
		if err!=nil{return zero,err}
		out=terminalReceiptWitness{Kind:v.Kind,Allocation:v.Allocation,GalleryIdentity:v.GalleryIdentity,PolicySHA256:v.PolicySHA256,
			Row:v.Row,PathID:v.PathID,FileIdentity:v.FileIdentity}
		raw,err=json.Marshal(v)
		if err!=nil{return zero,ErrStageRecovery}
	default:return zero,ErrStageRecovery
	}
	after,err:=boundedRegularBytes(p,selected.name,selected.limit)
	if err!=nil||!bytes.Equal(before,raw)||!bytes.Equal(after,raw)||out.Allocation!=grant||out.GalleryIdentity!=r.GalleryIdentity||
		!validDigest(out.PolicySHA256)||len(e.Files)!=1||out.Row.PostID!=e.Files[0].ID||out.Row.ImageID<=0||
		out.Row.Size!=e.Files[0].Size||out.Row.SHA256!=e.Files[0].SHA256||!validNamingRelative(out.Row.Name)||out.PathID<=0||
		out.FileIdentity==""||len(out.FileIdentity)>128{return zero,ErrStageRecovery}
	s,err:=a.scope(ctx,grant.Request)
	current,dbErr:=observedDB(r,g)
	if err!=nil||!matches(r,s)||dbErr!=nil||!sameLandingDB(current,l)||g.WatcherRunning()||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRefused}
	out.ReceiptSHA256=digestBytes(raw)
	if released{
		m,_,err:=readPreparedReleaseAttempt(p,r,e)
		if err!=nil||m.Witness!=out{return zero,ErrStageRecovery}
		if err:=verifyPreparedReleaseInventory(src,m,true);err!=nil{return zero,err}
		if retired{
			t,_,err:=readSealedRetirementAttempt(p,r,e)
			if err!=nil||t.Witness!=out{return zero,ErrStageRecovery}
		}
	}
	return out,nil
}
