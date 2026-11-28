package acquisitionstage

import(
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

func validDigest(s string)bool{
	b,err:=hex.DecodeString(s)
	return err==nil&&len(b)==sha256.Size&&hex.EncodeToString(b)==s
}
func validResultFile(f acquisitionplan.VerifiedTransferFile)bool{
	id,err:=strconv.ParseUint(f.ID,10,64)
	ext:=filepath.Ext(f.RelativeName)
	return err==nil&&id>0&&strconv.FormatUint(id,10)==f.ID&&
		f.RelativeName==f.ID+ext&&slices.Contains(contract.IngestExtensions,ext)&&
		f.Size>0&&uint64(f.Size)<=workerproto.MaxStageFileBytes&&validDigest(f.SHA256)&&
		f.SidecarSize>0&&f.SidecarSize<=workerproto.MaxMetadataPostBytes&&validDigest(f.SidecarSHA256)
}
func exactInventory(root *os.Root,files []acquisitionplan.VerifiedTransferFile)bool{
	entries,err:=list(root,2*len(files))
	if err!=nil||len(entries)!=2*len(files){return false}
	want:=make(map[string]bool,2*len(files))
	for _,f:=range files{want[f.RelativeName]=true;want[f.RelativeName+".json"]=true}
	for _,e:=range entries{
		if !e.Type().IsRegular()||!want[e.Name()]{return false}
		delete(want,e.Name())
	}
	return len(want)==0
}

// copySealedMedia is an isolated copy primitive, not a writer-cleanup
// attestation, launch or commit permission. Both roots are trusted local
// handles selected by the library; src is the actual cleaned media
// directory, dst a distinct fresh private directory outside scan trees.
// The caller independently supplies exact approved manifest and verified
// contained result only after writer cleanup. It must retain any partial
// destination for recovery. No production caller, no automatic deletion.
func copySealedMedia(ctx context.Context,grant workerproto.StageAllocation,manifest []byte,
	files []acquisitionplan.VerifiedTransferFile,src,dst *os.Root)([]acquisitionplan.VerifiedTransferFile,error){
	if ctx.Err()!=nil||src==nil||dst==nil||!workerproto.ValidStageAllocation(grant)||len(files)>int(grant.MaxFiles){return nil,ErrStageRefused}
	si,err:=src.Stat(".")
	if err!=nil{return nil,ErrStageRefused}
	di,err:=dst.Stat(".")
	if err!=nil||os.SameFile(si,di){return nil,ErrStageRefused}
	if entries,err:=list(dst,0);err!=nil||len(entries)!=0{return nil,ErrStageRecovery}
	verifier,err:=acquisitionplan.NewStrictManifestVerifier(grant.Request.Transfer,manifest)
	if err!=nil{return nil,ErrStageRefused}
	seen:=make(map[string]bool,len(files))
	var total uint64
	for _,f:=range files{
		if !validResultFile(f)||seen[f.ID]{return nil,ErrStageRefused}
		seen[f.ID]=true
		// Budget both media bytes and their native sidecars.
		total+=uint64(f.Size)+uint64(f.SidecarSize)
		if total>grant.MaxBytes{return nil,ErrStageLimit}
	}
	if !exactInventory(src,files){return nil,ErrStageRecovery}
	for _,f:=range files{
		if ctx.Err()!=nil{return nil,ErrStageRefused}
		// Verify sidecar independently against the original approved
		// manifest, as well as its byte identity from contained cleanup.
		var metadata bytes.Buffer
		if err:=readChecked(ctx,src,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256,&metadata);err!=nil{return nil,err}
		if err:=verifier.VerifySidecar(f.ID,metadata.Bytes());err!=nil{return nil,ErrStageRefused}
		if err:=writeChecked(ctx,dst,f.RelativeName,f.Size,f.SHA256,func(w io.Writer)error{
			return readChecked(ctx,src,f.RelativeName,f.Size,f.SHA256,w)
		});err!=nil{return nil,err}
		if err:=writeChecked(ctx,dst,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256,func(w io.Writer)error{
			_,err:=io.Copy(w,bytes.NewReader(metadata.Bytes()));return err
		});err!=nil{return nil,err}
	}
	if !exactInventory(src,files)||!exactInventory(dst,files)||ctx.Err()!=nil{return nil,ErrStageRecovery}
	// Recheck both inventories' exact contents after all writers close.
	for _,f:=range files{
		for _,root:=range []*os.Root{src,dst}{
			if err:=readChecked(ctx,root,f.RelativeName,f.Size,f.SHA256,io.Discard);err!=nil{return nil,err}
			if err:=readChecked(ctx,root,f.RelativeName+".json",f.SidecarSize,f.SidecarSHA256,io.Discard);err!=nil{return nil,err}
		}
	}
	syncDirectory(dst)
	if ctx.Err()!=nil{return nil,ErrStageRefused}
	return slices.Clone(files),nil
}

// Refuse symlinks, nonregular entries and unexpected hard links. Open
// relative to a trusted root, compare the opened object to Lstat, bound
// all reads to expected size, hash actual bytes and recheck afterward.
// This narrows races, but cannot defeat arbitrary same-user mutation.
func readChecked(ctx context.Context,root *os.Root,name string,size int64,digest string,out io.Writer)error{
	return readCheckedLinks(ctx,root,name,size,digest,out,1)
}
func readCheckedLinks(ctx context.Context,root *os.Root,name string,size int64,digest string,out io.Writer,links uint64)error{
	if ctx.Err()!=nil||!filepath.IsLocal(name)||filepath.Base(name)!=name||strings.ContainsAny(name,"/\\:")||size<1||!validDigest(digest){return ErrStageRefused}
	info,err:=root.Lstat(name)
	if err!=nil||!info.Mode().IsRegular()||info.Size()!=size{return ErrStageRecovery}
	f,err:=root.Open(name)
	if err!=nil{return ErrStageRecovery}
	defer f.Close()
	actual,err:=f.Stat()
	if err!=nil||!os.SameFile(info,actual)||actual.Size()!=size||!exactLinks(f,actual,links){return ErrStageRecovery}
	h:=sha256.New()
	writer:=io.MultiWriter(h,out)
	buf:=make([]byte,64<<10)
	var n int64
	for{
		if ctx.Err()!=nil{return ErrStageRefused}
		count,err:=f.Read(buf[:min(int64(len(buf)),size-n+1)])
		n+=int64(count)
		if n>size{return ErrStageRecovery}
		if count>0{
			w,e:=writer.Write(buf[:count])
			if e!=nil||w!=count{return ErrStageRecovery}
		}
		if err==io.EOF{break}
		if err!=nil||count==0{return ErrStageRecovery}
	}
	after,err:=root.Lstat(name)
	if err!=nil||!after.Mode().IsRegular()||!os.SameFile(info,after)||after.Size()!=size||after.ModTime()!=info.ModTime()||
		!exactLinks(f,after,links)||n!=size||hex.EncodeToString(h.Sum(nil))!=digest||ctx.Err()!=nil{return ErrStageRecovery}
	return nil
}
func writeChecked(ctx context.Context,root *os.Root,name string,size int64,digest string,write func(io.Writer)error)error{
	if ctx.Err()!=nil||!filepath.IsLocal(name)||filepath.Base(name)!=name||strings.ContainsAny(name,"/\\:"){return ErrStageRefused}
	f,err:=root.OpenFile(name,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600)
	if err!=nil{return ErrStageRecovery}
	err=write(f)
	if err==nil{err=f.Sync()}
	if closeErr:=f.Close();err==nil{err=closeErr}
	if err!=nil{return ErrStageRecovery}
	return readChecked(ctx,root,name,size,digest,io.Discard)
}
