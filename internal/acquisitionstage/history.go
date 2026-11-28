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
	if !validID(id){return zero,ErrStageRecovery}
	d,err:=openDir(history,id);if err!=nil{return zero,err};defer d.Close()
	entries,err:=list(d,2);if err!=nil||len(entries)!=2{return zero,ErrStageRecovery}
	raw,err:=boundedRegularBytes(d,"index.json",maxRecordBytes)
	var out HistoryEntry
	if err!=nil||json.Unmarshal(raw,&out)!=nil||out.Version!=1||out.Allocation.Request.OperationID!=id||!validDigest(out.TreeSHA256){return zero,ErrStageRecovery}
	body,err:=json.Marshal(out);if err!=nil||!bytes.Equal(raw,body){return zero,ErrStageRecovery}
	op,err:=openDir(d,"operation");if err!=nil{return zero,err};defer op.Close()
	r,err:=readRecord(op);if err!=nil||r.Allocation!=out.Allocation||r.StageRoot!=stage{return zero,ErrStageRecovery}
	e,err:=readSeal(op,r.Allocation);if err!=nil{return zero,err}
	if _,err:=readPublication(op,r,e);err!=nil{return zero,err}
	p,err:=openDir(op,"publication");if err!=nil{return zero,err};defer p.Close()
	c,err:=readOperationClosure(p,r,e);if err!=nil||c!=out.Closure{return zero,ErrStageRecovery}
	hash,err:=treeDigest(ctx,op);if err!=nil||hash!=out.TreeSHA256{return zero,ErrStageRecovery}
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
		if err!=nil||a.historyTransfers[v.Allocation.TransferID]||v.Closure.RetainedPayloadBytes>maxReservedBytes-a.historyCharge{return ErrStageRecovery}
		a.history[e.Name()]=v;a.historyTransfers[v.Allocation.TransferID]=true;a.historyCharge+=v.Closure.RetainedPayloadBytes
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
	raw,err:=json.Marshal(v);if err!=nil||len(raw)>maxRecordBytes{return zero,ErrStageLimit}
	if authorize(ctx,grant,c.Witness)!=nil||ctx.Err()!=nil{return zero,ErrStageRefused}
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
	if err!=nil||stored!=v{return zero,ErrStageRecovery}
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
