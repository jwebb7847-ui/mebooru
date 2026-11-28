package acquisitionstage

import(
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

const historyDirName="history"
const maxHistoryEntries=1000000
const maxHistoryTreeEntries=4096

// HistoryEntry preserves the exact closure and a digest of the retained
// operation directory. The archive index is rebuilt once on Open, not
// by rescanning all historical files before every new allocation.
type HistoryEntry struct{
	Version int
	Allocation workerproto.StageAllocation
	Closure operationClosure
	TreeSHA256 string
	BatchSHA256 string `json:",omitempty"`
	BatchCharge uint64 `json:",omitempty"`
}
type HistoryAuthority func(context.Context,workerproto.StageAllocation,OwnerReceipt)error

func treeDigest(ctx context.Context,root *os.Root)(string,error){
	h:=sha256.New()
	count:=0
	var walk func(*os.Root,string,int)error
	walk=func(dir *os.Root,prefix string,depth int)error{
		if depth>8||ctx.Err()!=nil{return ErrStageRecovery}
		entries,err:=list(dir,maxHistoryTreeEntries)
		if err!=nil{return err}
		sort.Slice(entries,func(i,j int)bool{return entries[i].Name()<entries[j].Name()})
		for _,entry:=range entries{
			count++;if count>maxHistoryTreeEntries{return ErrStageLimit}
			name:=entry.Name();rel:=prefix+name
			if !filepath.IsLocal(name)||filepath.Base(name)!=name{return ErrStageRecovery}
			info,err:=dir.Lstat(name);if err!=nil{return ErrStageRecovery}
			if info.IsDir(){
				raw,_:=json.Marshal([]any{rel,"directory"});h.Write(raw)
				child,err:=openDir(dir,name);if err!=nil{return err}
				err=walk(child,rel+"/",depth+1);child.Close();if err!=nil{return err}
				continue
			}
			if !info.Mode().IsRegular()||info.Size()<1||uint64(info.Size())>workerproto.MaxStageTotalBytes{return ErrStageRecovery}
			f,err:=dir.Open(name);if err!=nil{return err}
			actual,err:=f.Stat()
			if err!=nil||!os.SameFile(info,actual)||!singleLink(f,actual){f.Close();return ErrStageRecovery}
			fileHash:=sha256.New()
			n,err:=io.Copy(fileHash,io.LimitReader(f,info.Size()+1))
			after,afterErr:=dir.Lstat(name);linkOK:=afterErr==nil&&singleLink(f,after);f.Close()
			if err!=nil||n!=info.Size()||afterErr!=nil||!linkOK||!os.SameFile(info,after)||after.Size()!=info.Size()||after.ModTime()!=info.ModTime()||ctx.Err()!=nil{return ErrStageRecovery}
			raw,_:=json.Marshal([]any{rel,n,hex.EncodeToString(fileHash.Sum(nil))});h.Write(raw)
		}
		return nil
	}
	if err:=walk(root,"",0);err!=nil{return "",err}
	return hex.EncodeToString(h.Sum(nil)),nil
}
func readHistoryEntry(ctx context.Context,history *os.Root,id,stage string)(HistoryEntry,error){
	var zero HistoryEntry
	if !validID(id){return zero,fmt.Errorf("%w: unexpected archive entry %q",ErrStageRecovery,id)}
	d,err:=openDir(history,id);if err!=nil{return zero,fmt.Errorf("archive envelope: %w",err)};defer d.Close()
	entries,err:=list(d,2);if err!=nil||len(entries)!=2{return zero,fmt.Errorf("%w: archive envelope must contain index.json and operation",ErrStageRecovery)}
	raw,err:=boundedRegularBytes(d,"index.json",maxRecordBytes)
	var out HistoryEntry
	if err!=nil||json.Unmarshal(raw,&out)!=nil||out.Version!=1||out.Allocation.Request.OperationID!=id||!validDigest(out.TreeSHA256){return zero,fmt.Errorf("%w: archive index is missing or invalid",ErrStageRecovery)}
	body,err:=json.Marshal(out);if err!=nil||!bytes.Equal(raw,body){return zero,fmt.Errorf("%w: archive index encoding changed",ErrStageRecovery)}
	op,err:=openDir(d,"operation");if err!=nil{return zero,fmt.Errorf("archived operation directory: %w",err)};defer op.Close()
	r,err:=readRecord(op);if err!=nil||r.Allocation!=out.Allocation{return zero,fmt.Errorf("%w: archive allocation differs from index",ErrStageRecovery)}
	if r.StageRoot!=stage{return zero,fmt.Errorf("%w: archive allocation belongs to another staging path",ErrStageRecovery)}
	// Archived children are completed read-only evidence. The tree was
	// checked before it was moved here. Verify its exact bytes before
	// decoding terminal records that contain pre-reboot device identities.
	hash,err:=treeDigest(ctx,op);if err!=nil{return zero,fmt.Errorf("archive tree read: %w",err)}
	if hash!=out.TreeSHA256{return zero,fmt.Errorf("%w: archive tree digest differs from index",ErrStageRecovery)}
	e,err:=readSealMode(op,r.Allocation,out.BatchSHA256=="");if err!=nil{return zero,fmt.Errorf("archive sealed inventory: %w",err)}
	if out.BatchSHA256!=""{
		if !validDigest(out.BatchSHA256)||out.Closure!=(operationClosure{}){return zero,ErrStageRecovery}
		v,charge,err:=archivableBatch(ctx,op,r,e)
		if err!=nil{return zero,fmt.Errorf("archive batch receipts and retained files: %w",err)}
		if charge!=out.BatchCharge||v!=out.BatchSHA256{return zero,fmt.Errorf("%w: archived batch receipt differs from index",ErrStageRecovery)}
	}else{
		if out.BatchCharge!=0{return zero,ErrStageRecovery}
		if _,err:=readPublicationMode(op,r,e,true);err!=nil{return zero,fmt.Errorf("archive publication: %w",err)}
		p,err:=openDir(op,"publication");if err!=nil{return zero,err};defer p.Close()
		c,err:=readOperationClosure(p,r,e);if err!=nil||c!=out.Closure{return zero,fmt.Errorf("%w: archive closure differs from index",ErrStageRecovery)}
	}
	return out,nil
}
func(a *Allocator) historyLive()bool{
	if a.historyPoisoned||a.root==nil{return false}
	info,err:=a.root.Lstat(historyDirName)
	if a.historyInfo==nil{return errors.Is(err,os.ErrNotExist)}
	return err==nil&&info.IsDir()&&os.SameFile(info,a.historyInfo)
}
func(a *Allocator) loadHistory()error{
	info,err:=a.root.Lstat(historyDirName)
	if errors.Is(err,os.ErrNotExist){return nil}
	if err!=nil||!info.IsDir(){return ErrStageRecovery}
	h,err:=openDir(a.root,historyDirName);if err!=nil{return err};defer h.Close()
	entries,err:=list(h,maxHistoryEntries);if err!=nil{return err}
	for _,e:=range entries{
		v,err:=readHistoryEntry(context.Background(),h,e.Name(),a.path)
		if err!=nil{return fmt.Errorf("history/%s: %w",e.Name(),err)}
		if a.historyTransfers[v.Allocation.TransferID]{return fmt.Errorf("%w: duplicate transfer in history/%s",ErrStageRecovery,e.Name())}
		if v.charge()>maxReservedBytes-a.historyCharge{return fmt.Errorf("%w: archived reservation exceeds capacity at history/%s",ErrStageRecovery,e.Name())}
		a.history[e.Name()]=v;a.historyTransfers[v.Allocation.TransferID]=true;a.historyCharge+=v.charge()
	}
	a.historyInfo=info
	return nil
}

// ArchiveClosed moves a fully reclaimed operation, not a live or
// quarantined one. Preserve every byte. An interrupted envelope blocks
// opening the allocator rather than permitting identity reuse.
func(a *Allocator) ArchiveClosed(ctx context.Context,grant workerproto.StageAllocation,authorize HistoryAuthority)(HistoryEntry,error){
	a.mu.Lock();defer a.mu.Unlock()
	var zero HistoryEntry
	if authorize==nil||ctx.Err()!=nil||!a.live()||!a.historyLive()||len(a.history)>=maxHistoryEntries{return zero,ErrStageRefused}
	if _,ok:=a.history[grant.Request.OperationID];ok{return zero,ErrStageRefused}
	r,err:=a.load(grant.Request.OperationID);if err!=nil||r.Allocation!=grant{return zero,ErrStageRefused}
	done,charge,err:=a.closedCharge(ctx,r);if err!=nil||!done{return zero,ErrStageRefused}
	op,err:=openDir(a.root,grant.Request.OperationID);if err!=nil{return zero,err}
	e,err:=readSeal(op,grant);if err!=nil{op.Close();return zero,err}
	p,err:=openDir(op,"publication");if err!=nil{op.Close();return zero,err}
	c,err:=readOperationClosure(p,r,e);p.Close()
	if err!=nil||charge!=c.RetainedPayloadBytes||authorize(ctx,grant,c.Witness)!=nil{op.Close();return zero,ErrStageRefused}
	hash,err:=treeDigest(ctx,op);op.Close()
	if err!=nil||charge>maxReservedBytes-a.historyCharge{return zero,ErrStageRecovery}
	v:=HistoryEntry{Version:1,Allocation:grant,Closure:c,TreeSHA256:hash}
	if authorize(ctx,grant,c.Witness)!=nil||ctx.Err()!=nil{return zero,ErrStageRefused}
	return a.archiveEvidenceLocked(ctx,v)
}
func(v HistoryEntry)charge()uint64{
	if v.BatchSHA256!=""{return v.BatchCharge}
	return v.Closure.RetainedPayloadBytes
}
func(a *Allocator) archiveEvidenceLocked(ctx context.Context,v HistoryEntry)(HistoryEntry,error){
	var zero HistoryEntry
	grant:=v.Allocation
	charge:=v.charge()
	if charge>maxReservedBytes-a.historyCharge{return zero,ErrStageLimit}
	raw,err:=json.Marshal(v);if err!=nil||len(raw)>maxRecordBytes{return zero,ErrStageLimit}
	a.historyPoisoned=true // failure after this point blocks all new grants until reopen/reconciliation
	if a.historyInfo==nil{
		if err:=a.root.Mkdir(historyDirName,0700);err!=nil{return zero,ErrStageRecovery}
		a.historyInfo,err=a.root.Lstat(historyDirName);if err!=nil{return zero,ErrStageRecovery}
		syncDirectory(a.root)
	}
	h,err:=openDir(a.root,historyDirName);if err!=nil{return zero,err};defer h.Close()
	id:=grant.Request.OperationID
	if err:=h.Mkdir(id,0700);err!=nil{return zero,ErrStageRecovery}
	syncDirectory(h)
	d,err:=openDir(h,id);if err!=nil{return zero,err}
	if err:=writeOwnerBytes(ctx,d,"index.json",raw);err!=nil{d.Close();return zero,err}
	syncDirectory(d);d.Close()
	// No open handle to the operation remains when moving on Windows.
	if err:=a.root.Rename(id,filepath.Join(historyDirName,id,"operation"));err!=nil{return zero,ErrStageRecovery}
	syncDirectory(a.root);syncDirectory(h)
	stored,err:=readHistoryEntry(ctx,h,id,a.path)
	if err!=nil{return zero,fmt.Errorf("history/%s after archive: %w",id,err)}
	if stored!=v{return zero,fmt.Errorf("%w: history/%s readback differs",ErrStageRecovery,id)}
	a.history[id]=v;a.historyTransfers[grant.TransferID]=true;a.historyCharge+=charge;a.historyPoisoned=false
	return v,nil
}
func(a *Allocator) HistoricalReceipt(ctx context.Context,id string)(HistoryEntry,error){
	a.mu.Lock();defer a.mu.Unlock()
	var zero HistoryEntry
	want,ok:=a.history[id]
	if !ok||ctx.Err()!=nil||!a.live()||!a.historyLive(){return zero,ErrStageRefused}
	h,err:=openDir(a.root,historyDirName);if err!=nil{return zero,err};defer h.Close()
	got,err:=readHistoryEntry(ctx,h,id,a.path)
	if err!=nil||got!=want{return zero,ErrStageRecovery}
	return got,nil
}


func archivableBatch(ctx context.Context,op *os.Root,r record,e SealedEvidence)(string,uint64,error){
	p,err:=readBatchPlan(op,r,e);if err!=nil{return "",0,err}
	b,err:=openDir(op,"batch");if err!=nil{return "",0,err};defer b.Close()
	out,err:=readBatchResult(b,p);if err!=nil{return "",0,err}
	m,err:=readBatchReclaim(b,p,out,true);if err!=nil{return "",0,err}
	if err:=verifyBatchPrivate(ctx,op,r,e,true,true);err!=nil{return "",0,err}
	return m.ResultSHA256,m.RetainedSidecarBytes,nil
}

// ArchiveBatch preserves a completed/reclaimed parent and its complete
// aggregate, preventing every normal window from consuming an active
// inventory entry indefinitely. No deletion or re-download permission.
func(a *Allocator) ArchiveBatch(ctx context.Context,parent workerproto.StageAllocation,authorize BatchReclaimAuthority)(HistoryEntry,error){
	a.mu.Lock();defer a.mu.Unlock()
	var zero HistoryEntry
	if authorize==nil||ctx.Err()!=nil||!a.live()||!a.historyLive()||len(a.history)>=maxHistoryEntries{return zero,ErrStageRefused}
	if _,ok:=a.history[parent.Request.OperationID];ok{return zero,ErrStageRefused}
	r,err:=a.load(parent.Request.OperationID);if err!=nil||r.Allocation!=parent{return zero,ErrStageRefused}
	op,err:=openDir(a.root,parent.Request.OperationID);if err!=nil{return zero,err}
	v,err:=func()(HistoryEntry,error){
		defer op.Close()
		e,err:=readSeal(op,parent);if err!=nil{return zero,err}
		hash,charge,err:=archivableBatch(ctx,op,r,e);if err!=nil{return zero,err}
		p,err:=readBatchPlan(op,r,e);if err!=nil{return zero,err}
		b,err:=openDir(op,"batch");if err!=nil{return zero,err};defer b.Close()
		out,err:=readBatchResult(b,p);if err!=nil{return zero,err}
		if err:=a.verifyBatchChildrenLocked(ctx,p,out);err!=nil{return zero,err}
		if authorize(ctx,parent,out)!=nil{return zero,ErrStageRefused}
		tree,err:=treeDigest(ctx,op);if err!=nil{return zero,err}
		if authorize(ctx,parent,out)!=nil{return zero,ErrStageRefused}
		return HistoryEntry{Version:1,Allocation:parent,TreeSHA256:tree,BatchSHA256:hash,BatchCharge:charge},nil
	}()
	if err!=nil{return zero,err}
	return a.archiveEvidenceLocked(ctx,v)
}
func(a *Allocator) historicalBatchLocked(ctx context.Context,parent workerproto.StageAllocation)(BatchOutcome,error){
	var zero BatchOutcome
	v,ok:=a.history[parent.Request.OperationID]
	if !ok||v.Allocation!=parent||v.BatchSHA256==""||!a.historyLive(){return zero,ErrStageRefused}
	h,err:=openDir(a.root,historyDirName);if err!=nil{return zero,err};defer h.Close()
	got,err:=readHistoryEntry(ctx,h,parent.Request.OperationID,a.path);if err!=nil||got!=v{return zero,ErrStageRecovery}
	dir,err:=openDir(h,parent.Request.OperationID);if err!=nil{return zero,err};defer dir.Close()
	op,err:=openDir(dir,"operation");if err!=nil{return zero,err};defer op.Close()
	r,err:=readRecord(op);if err!=nil{return zero,err}
	e,err:=readSeal(op,parent);if err!=nil{return zero,err}
	p,err:=readBatchPlan(op,r,e);if err!=nil{return zero,err}
	b,err:=openDir(op,"batch");if err!=nil{return zero,err};defer b.Close()
	out,err:=readBatchResult(b,p);if err!=nil{return zero,err}
	if err:=a.verifyBatchChildrenLocked(ctx,p,out);err!=nil{return zero,err}
	return out,nil
}
