package processguard

import(
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// A cooperative sampled guard, not a filesystem quota. It counts all
// regular payload/sidecar/temporary entries, including .part files.
// Strict verification still checks the exact final inventory and hashes.
type transferWorkspaceBudget struct{
	path string
	root,media *os.Root
	grant workerproto.StageAllocation
	failed atomic.Bool
}
func openTransferWorkspaceBudget(path string,grant workerproto.StageAllocation)(*transferWorkspaceBudget,error){
	info,err:=os.Lstat(path)
	if err!=nil||!info.IsDir(){return nil,ErrStrictTransferOperation}
	root,err:=os.OpenRoot(path)
	if err!=nil{return nil,ErrStrictTransferOperation}
	current,err:=root.Stat(".")
	if err!=nil||!os.SameFile(info,current){root.Close();return nil,ErrStrictTransferOperation}
	mi,err:=root.Lstat("media")
	if err!=nil||!mi.IsDir(){root.Close();return nil,ErrStrictTransferOperation}
	media,err:=root.OpenRoot("media")
	if err!=nil{root.Close();return nil,ErrStrictTransferOperation}
	actual,err:=media.Stat(".")
	if err!=nil||!os.SameFile(mi,actual){media.Close();root.Close();return nil,ErrStrictTransferOperation}
	return &transferWorkspaceBudget{path:path,root:root,media:media,grant:grant},nil
}
func(b *transferWorkspaceBudget)Close(){b.media.Close();b.root.Close()}
func(b *transferWorkspaceBudget)check(ctx context.Context)bool{
	if b.failed.Load()||ctx.Err()!=nil{return false}
	if !b.measure(){b.failed.Store(true);return false}
	return !b.failed.Load()&&ctx.Err()==nil
}
func(b *transferWorkspaceBudget)measure()bool{
	live,err:=os.Lstat(b.path)
	if err!=nil||!live.IsDir(){return false}
	held,err:=b.root.Stat(".")
	if err!=nil||!os.SameFile(live,held){return false}
	current,err:=b.root.Lstat("media")
	if err!=nil||!current.IsDir(){return false}
	m,err:=b.media.Stat(".")
	if err!=nil||!os.SameFile(current,m){return false}
	// The two bounded manifest files are protocol overhead. No other
	// workspace-root entry is permitted.
	r,err:=b.root.Open(".")
	if err!=nil{return false}
	entries,err:=r.ReadDir(4)
	r.Close()
	if err!=nil&&err!=io.EOF||len(entries)!=3{return false}
	for _,e:=range entries{
		if e.Name()=="media"{if !e.IsDir(){return false};continue}
		if e.Name()!="strict-blacklist.json"&&e.Name()!="strict-blacklist-probe.json"{return false}
		i,err:=b.root.Lstat(e.Name())
		if err!=nil||!i.Mode().IsRegular()||i.Size()<1||i.Size()>workerproto.MaxMetadataOutputBytes{return false}
	}
	f,err:=b.media.Open(".")
	if err!=nil{return false}
	defer f.Close()
	// At most one final media, one sidecar, and one transient entry
	// per approved file. Every transient byte counts against MaxBytes.
	limit:=int(b.grant.MaxFiles)*3
	entries,err=f.ReadDir(limit+1)
	if err!=nil&&err!=io.EOF||len(entries)>limit{return false}
	var total uint64
	mediaCount,sidecarCount:=0,0
	for _,e:=range entries{
		name:=e.Name()
		if filepath.Base(name)!=name||!filepath.IsLocal(name){return false}
		i,err:=b.media.Lstat(name)
		// A downloader may atomically rename a .part between enumeration
		// and stat. The next sample and final exact verification reconcile it.
		if errors.Is(err,os.ErrNotExist){continue}
		if err!=nil||!i.Mode().IsRegular()||i.Size()<0{return false}
		n:=uint64(i.Size())
		if n>workerproto.MaxStageFileBytes||n>b.grant.MaxBytes-total{return false}
		total+=n
		if strings.HasSuffix(name,".json"){
			sidecarCount++
			if n>workerproto.MaxMetadataPostBytes{return false}
		}else if !strings.HasSuffix(name,".part"){mediaCount++}
		if mediaCount>int(b.grant.MaxFiles)||sidecarCount>int(b.grant.MaxFiles){return false}
	}
	return true
}
func(b *transferWorkspaceBudget)monitor(ctx context.Context,stop <-chan struct{},cancel context.CancelFunc){
	t:=time.NewTicker(100*time.Millisecond)
	defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case <-stop:return
		case <-t.C:
			if !b.check(ctx){cancel();return}
		}
	}
}
func allocatedTransferResultFits(g workerproto.StageAllocation,files []acquisitionplan.VerifiedTransferFile)bool{
	if len(files)>int(g.MaxFiles){return false}
	var total uint64
	for _,f:=range files{
		if f.Size<=0||f.SidecarSize<=0||uint64(f.Size)>workerproto.MaxStageFileBytes{return false}
		n:=uint64(f.Size)+uint64(f.SidecarSize)
		if n>g.MaxBytes-total{return false}
		total+=n
	}
	return true
}
