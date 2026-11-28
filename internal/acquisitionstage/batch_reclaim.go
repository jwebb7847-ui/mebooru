package acquisitionstage

import(
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

// Reclaim redundant batch parent copies only after exact child closures
// and actual final bytes have been verified. Retain provenance and receipts.
type BatchReclaimAuthority func(context.Context,workerproto.StageAllocation,BatchOutcome)error
type batchReclaimRecord struct{
	Version int
	ResultSHA256 string
	RetainedSidecarBytes uint64
}
func expectedBatchReclaim(p batchPlan,out BatchOutcome)(batchReclaimRecord,error){
	var zero batchReclaimRecord
	body,err:=json.Marshal(out);if err!=nil||len(out.Items)!=len(p.Items){return zero,ErrStageRecovery}
	var n uint64
	for _,i:=range p.Items{if i.File.ID!=""{n+=uint64(i.File.SidecarSize)}}
	if n>p.Parent.MaxBytes{return zero,ErrStageRecovery}
	return batchReclaimRecord{Version:1,ResultSHA256:digestBytes(body),RetainedSidecarBytes:n},nil
}
func readBatchReclaim(b *os.Root,p batchPlan,out BatchOutcome,result bool)(batchReclaimRecord,error){
	var zero batchReclaimRecord
	want,err:=expectedBatchReclaim(p,out);if err!=nil{return zero,err}
	body,err:=json.Marshal(want);if err!=nil{return zero,err}
	raw,err:=boundedRegularBytes(b,"reclaim-attempt.json",maxRecordBytes)
	if err!=nil||!bytes.Equal(raw,body){return zero,ErrStageRecovery}
	if result{
		raw,err=boundedRegularBytes(b,"reclaim-result.json",maxRecordBytes)
		if err!=nil||!bytes.Equal(raw,body){return zero,ErrStageRecovery}
	}
	return want,nil
}
func batchMediaSubset(ctx context.Context,media *os.Root,files []acquisitionplan.VerifiedTransferFile,workspace,partial,complete,verify bool)error{
	entries,err:=list(media,2*len(files));if err!=nil{return err}
	type fact struct{size int64;hash string}
	want:=map[string]fact{}
	required:=map[string]bool{}
	for _,f:=range files{
		if !complete{want[f.RelativeName]=fact{f.Size,f.SHA256};if !partial{required[f.RelativeName]=true}}
		if !workspace||!complete{n:=f.RelativeName+".json";want[n]=fact{f.SidecarSize,f.SidecarSHA256};if !workspace||!partial{required[n]=true}}
	}
	for _,x:=range entries{
		v,ok:=want[x.Name()];if !ok||!x.Type().IsRegular(){return ErrStageRecovery}
		info,err:=media.Lstat(x.Name());if err!=nil||info.Size()!=v.size{return ErrStageRecovery}
		if verify{if err:=readChecked(ctx,media,x.Name(),v.size,v.hash,io.Discard);err!=nil{return err}}
		delete(required,x.Name())
	}
	if len(required)!=0{return ErrStageRecovery}
	return nil
}
func checkBatchSealedInventory(op,media *os.Root,grant workerproto.StageAllocation,e SealedEvidence)error{
	b,err:=openDir(op,"batch");if err!=nil{return err};defer b.Close()
	if _,err:=b.Lstat("reclaim-attempt.json");errors.Is(err,os.ErrNotExist){
		if _,err:=b.Lstat("reclaim-result.json");!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
		if !exactInventory(media,e.Files){return ErrStageRecovery};return nil
	}else if err!=nil{return ErrStageRecovery}
	r,err:=readRecord(op);if err!=nil||r.Allocation!=grant{return ErrStageRecovery}
	p,err:=readBatchPlan(op,r,e);if err!=nil{return err}
	out,err:=readBatchResult(b,p);if err!=nil{return err}
	_,err=b.Lstat("reclaim-result.json");complete:=err==nil
	if err!=nil&&!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	if _,err:=readBatchReclaim(b,p,out,complete);err!=nil{return err}
	return batchMediaSubset(context.Background(),media,e.Files,false,true,complete,false)
}
func verifyBatchPrivate(ctx context.Context,op *os.Root,r record,e SealedEvidence,partial,complete bool)error{
	w,err:=openDir(op,r.Allocation.TransferID);if err!=nil{return err};defer w.Close()
	entries,err:=list(w,3);if err!=nil||len(entries)!=3{return ErrStageRecovery}
	m,err:=boundedRegularBytes(w,"strict-blacklist.json",workerproto.MaxMetadataOutputBytes)
	if err!=nil||digestBytes(m)!=r.Allocation.Request.Transfer.ManifestSHA256{return ErrStageRecovery}
	if _,err:=boundedRegularBytes(w,"strict-blacklist-probe.json",workerproto.MaxMetadataOutputBytes);err!=nil{return err}
	wm,err:=openDir(w,"media");if err!=nil{return err};defer wm.Close()
	if err:=batchMediaSubset(ctx,wm,e.Files,true,partial,complete,true);err!=nil{return err}
	s,err:=openDir(op,"sealed");if err!=nil{return err};defer s.Close()
	sm,err:=openDir(s,"media");if err!=nil{return err};defer sm.Close()
	if err:=batchMediaSubset(ctx,sm,e.Files,false,partial,complete,true);err!=nil{return err}
	verifier,err:=acquisitionplan.NewStrictManifestVerifier(r.Allocation.Request.Transfer,m);if err!=nil{return err}
	for _,f:=range e.Files{
		n,err:=boundedRegularBytes(sm,f.RelativeName+".json",workerproto.MaxMetadataPostBytes)
		if err!=nil||digestBytes(n)!=f.SidecarSHA256||verifier.VerifySidecar(f.ID,n)!=nil{return ErrStageRecovery}
	}
	return nil
}

// ReclaimBatch accepts a new intent, or explicit resume of a prior
// intent. Missing private payload is accepted only after that intent.
func(a *Allocator) ReclaimBatch(ctx context.Context,parent workerproto.StageAllocation,set *library.Set,authorize BatchReclaimAuthority,resume bool)error{
	if set==nil||authorize==nil{return ErrStageRefused}
	return set.WithAcquisitionFence(ctx,parent.Request.Transfer.Metadata.Gallery,func(g *library.Gallery,fence func()error)error{
		a.mu.Lock();defer a.mu.Unlock()
		if fence()!=nil||!a.live(){return ErrStageRefused}
		r,err:=a.load(parent.Request.OperationID);if err!=nil||r.Allocation!=parent||r.GalleryRoot!=g.GalleryPath{return ErrStageRefused}
		op,err:=openDir(a.root,parent.Request.OperationID);if err!=nil{return err};defer op.Close()
		e,err:=readSeal(op,parent);if err!=nil{return err}
		p,err:=readBatchPlan(op,r,e);if err!=nil{return err}
		b,err:=openDir(op,"batch");if err!=nil{return err};defer b.Close()
		out,err:=readBatchResult(b,p);if err!=nil{return err}
		if _,err:=b.Lstat("reclaim-result.json");!errors.Is(err,os.ErrNotExist){return ErrStageRefused}
		if resume{if _,err:=readBatchReclaim(b,p,out,false);err!=nil{return err}
		}else if _,err:=b.Lstat("reclaim-attempt.json");!errors.Is(err,os.ErrNotExist){return ErrStageRefused}
		dst,err:=os.OpenRoot(r.GalleryRoot);if err!=nil{return ErrStageRecovery};defer dst.Close()
		current:=func()error{
			if fence()!=nil||authorize(ctx,parent,out)!=nil||ctx.Err()!=nil||!a.live(){return ErrStageRefused}
			s,err:=a.scope(ctx,parent.Request);if err!=nil||!matches(r,s){return ErrStageRefused}
			return nil
		}
		checkItem:=func(i int)error{
			v:=out.Items[i]
			if v.State=="not_transferred"{return nil}
			if v.State!="committed"||v.Receipt==nil||v.Receipt.GalleryIdentity!=r.GalleryIdentity{return ErrStageRecovery}
			w:=v.Receipt
			child,err:=a.batchChildWitnessLocked(ctx,p.Items[i]);if err!=nil||child!=*w{return ErrStageRecovery}
			if g.Boundary().Check(filepath.Join(r.GalleryRoot,w.Row.Name))!=nil{return ErrStageRefused}
			id,err:=verifyHeldCanonical(ctx,dst,w.Row.Name,p.Items[i].File)
			if err!=nil||id!=w.FileIdentity{return ErrStageRecovery}
			var n int
			if err:=g.DB.Read.QueryRowContext(ctx,`SELECT COUNT(*) FROM images WHERE id=? AND canonical_path=? AND sha256=? AND is_missing=0`,w.Row.ImageID,filepath.Join(r.GalleryRoot,w.Row.Name),w.Row.SHA256).Scan(&n);err!=nil||n!=1{return ErrStageRecovery}
			return nil
		}
		check:=func()error{
			if err:=current();err!=nil{return err}
			for i:=range out.Items{if err:=checkItem(i);err!=nil{return err}}
			return nil
		}
		if err:=check();err!=nil{return err}
		if err:=verifyBatchPrivate(ctx,op,r,e,resume,false);err!=nil{return err}
		m,err:=expectedBatchReclaim(p,out);if err!=nil{return err}
		body,err:=json.Marshal(m);if err!=nil{return err}
		if !resume{if err:=writeOwnerBytes(ctx,b,"reclaim-attempt.json",body);err!=nil{return err};syncDirectory(b)}
		w,err:=openDir(op,parent.TransferID);if err!=nil{return err};defer w.Close()
		wm,err:=openDir(w,"media");if err!=nil{return err};defer wm.Close()
		sealed,err:=openDir(op,"sealed");if err!=nil{return err};defer sealed.Close()
		sm,err:=openDir(sealed,"media");if err!=nil{return err};defer sm.Close()
		for i,item:=range p.Items{
			if item.File.ID==""{continue}
			f:=item.File
			if err:=current();err!=nil{return err}
			if err:=checkItem(i);err!=nil{return err}
			for _,v:=range []struct{root *os.Root;name string;size int64;hash string}{{wm,f.RelativeName,f.Size,f.SHA256},{wm,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256},{sm,f.RelativeName,f.Size,f.SHA256}}{
				if _,err:=v.root.Lstat(v.name);errors.Is(err,os.ErrNotExist){if !resume{return ErrStageRecovery};continue}else if err!=nil{return ErrStageRecovery}
				if err:=readChecked(ctx,v.root,v.name,v.size,v.hash,io.Discard);err!=nil{return err}
				if err:=v.root.Remove(v.name);err!=nil{return ErrStageRecovery}
				syncDirectory(v.root)
			}
		}
		if err:=check();err!=nil{return err}
		if err:=verifyBatchPrivate(ctx,op,r,e,true,true);err!=nil{return err}
		if err:=writeOwnerBytes(ctx,b,"reclaim-result.json",body);err!=nil{return err};syncDirectory(b)
		return nil
	})
}
