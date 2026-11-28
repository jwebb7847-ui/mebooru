package acquisitionstage

import(
	"bytes"
	"context"
	"io"
	"os"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// OpenForDestination chooses only an existing owner-approved private
// root on the actual destination filesystem. No network or gallery
// mutation and no implicit fallback to an unsafe location.
func OpenForDestination(ctx context.Context,roots []string,maxCount int,maxBytes uint64,authority Authority,q workerproto.StageAllocateRequest)(*Allocator,error){
	if len(roots)==0||len(roots)>32||authority==nil{return nil,ErrStageRefused}
	for _,path:=range roots{
		a,err:=Open(path,maxCount,maxBytes,authority)
		if err!=nil{continue}
		if _,err:=a.scope(ctx,q);err==nil{return a,nil}
		a.Close()
	}
	return nil,ErrStageRefused
}

// AdoptVerifiedWorkspace copies a cleaned, independently owned source
// workspace into a destination-local allocation. It works across
// volumes because all payloads are copied and verified, never renamed.
// The source remains untouched; its owner decides later reclamation.
func(a *Allocator) AdoptVerifiedWorkspace(ctx context.Context,grant workerproto.StageAllocation,source string,observe SealObserver)(SealedEvidence,error){
	var zero SealedEvidence
	if observe==nil{return zero,ErrStageRefused}
	manifest,files,err:=observe(ctx,grant)
	if err!=nil||!validSeal(SealedEvidence{Version:1,Allocation:grant,Files:files},grant){return zero,ErrStageRefused}
	a.mu.Lock()
	err=func()error{
		s,err:=a.scope(ctx,grant.Request);if err!=nil{return err}
		_,sourceInfo,err:=directory(source);if err!=nil{return err}
		for _,p:=range append(append([]string{},s.GalleryRoots...),a.path){
			_,info,err:=directory(p);if err!=nil{return err}
			inside,err:=contains(info,source);if err!=nil||inside{return ErrStageRefused}
			inside,err=contains(sourceInfo,p);if err!=nil||inside{return ErrStageRefused}
		}
		r,err:=a.load(grant.Request.OperationID);if err!=nil||r.Allocation!=grant||!matches(r,s){return ErrStageRefused}
		src,err:=os.OpenRoot(source);if err!=nil{return ErrStageRecovery};defer src.Close()
		actual,err:=src.Stat(".");if err!=nil||!os.SameFile(sourceInfo,actual){return ErrStageRecovery}
		entries,err:=list(src,3);if err!=nil||len(entries)!=3{return ErrStageRecovery}
		raw,err:=boundedRegularBytes(src,"strict-blacklist.json",workerproto.MaxMetadataOutputBytes)
		if err!=nil||!bytes.Equal(raw,manifest)||digestBytes(raw)!=grant.Request.Transfer.ManifestSHA256{return ErrStageRecovery}
		probe,err:=boundedRegularBytes(src,"strict-blacklist-probe.json",workerproto.MaxMetadataOutputBytes);if err!=nil{return err}
		sm,err:=openDir(src,"media");if err!=nil{return err};defer sm.Close()
		op,err:=openDir(a.root,grant.Request.OperationID);if err!=nil{return err};defer op.Close()
		w,err:=openDir(op,grant.TransferID);if err!=nil{return err};defer w.Close()
		if entries,err:=list(w,0);err!=nil||len(entries)!=0{return ErrStageRecovery}
		if err:=writeOwnerBytes(ctx,w,"strict-blacklist.json",raw);err!=nil{return err}
		if err:=writeOwnerBytes(ctx,w,"strict-blacklist-probe.json",probe);err!=nil{return err}
		if err:=w.Mkdir("media",0700);err!=nil{return ErrStageRecovery}
		dm,err:=openDir(w,"media");if err!=nil{return err};defer dm.Close()
		if _,err:=copySealedMedia(ctx,grant,manifest,files,sm,dm);err!=nil{return err}
		for _,f:=range files{
			if err:=readChecked(ctx,dm,f.RelativeName,f.Size,f.SHA256,io.Discard);err!=nil{return err}
		}
		syncDirectory(w)
		return nil
	}()
	a.mu.Unlock()
	if err!=nil{return zero,err}
	return a.Seal(ctx,grant,func(ctx context.Context,q workerproto.StageAllocation)([]byte,[]acquisitionplan.VerifiedTransferFile,error){
		m,f,err:=observe(ctx,q)
		if err!=nil||!bytes.Equal(m,manifest)||len(f)!=len(files){return nil,nil,ErrStageRefused}
		for i:=range f{if f[i]!=files[i]{return nil,nil,ErrStageRefused}}
		return manifest,files,nil
	})
}
