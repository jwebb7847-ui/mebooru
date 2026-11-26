package acquisitionplan

import(
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// VerifiedTransferFile is isolated result evidence, not a durable receipt
// or permission to ingest. RelativeName is local to the fresh media root.
// Chapter II.6.4c must independently bind rooted staging and final commit.
type VerifiedTransferFile struct{
	ID string
	RelativeName string
	Size int64
	SHA256 string
}

const maxTransferFileBytes int64=512<<20
const maxTransferTotalBytes int64=1<<30

// VerifyTransferResult is called only after exact owned cleanup. Require
// a flat inventory of regular media/sidecar pairs, each printed once by
// the runtime and bound to an approved post. Changed/missing/extra files
// or evidence reject the entire candidate result. Missing approved posts
// may legitimately have changed and been rejected before download.
// This is point-in-time QA verification, not hostile same-user protection.
func(c TransferCommands)VerifyTransferResult(ctx context.Context,stdout []byte)([]VerifiedTransferFile,error){
	if ctx.Err()!=nil||!c.VerifyFiles()||!workerproto.ValidTransferPlan(c.request)||
		len(stdout)>workerproto.MaxMetadataOutputBytes{return nil,ErrStrictTransferPlan}
	actual,err:=filepath.EvalSymlinks(c.mediaRoot)
	if err!=nil||actual!=c.mediaRoot{return nil,ErrStrictTransferPlan}
	info,err:=os.Lstat(c.mediaRoot)
	if err!=nil||!info.IsDir(){return nil,ErrStrictTransferPlan}
	var approved map[string]map[string]json.RawMessage
	if json.Unmarshal(c.manifest,&approved)!=nil||len(approved)==0{return nil,ErrStrictTransferPlan}
	entries,err:=os.ReadDir(c.mediaRoot)
	if err!=nil||len(entries)>2*len(approved){return nil,ErrStrictTransferPlan}
	inventory:=make(map[string]bool,len(entries))
	for _,entry:=range entries{
		if !entry.Type().IsRegular(){return nil,ErrStrictTransferPlan}
		inventory[entry.Name()]=true
	}
	seen:=make(map[string]bool)
	out:=make([]VerifiedTransferFile,0)
	var total int64
	sc:=bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte,4096),64<<10)
	for sc.Scan(){
		if ctx.Err()!=nil{return nil,ErrStrictTransferPlan}
		line:=strings.TrimSuffix(sc.Text(),"\r")
		if line==""{continue}
		if !filepath.IsAbs(line)||filepath.Clean(line)!=line||filepath.Dir(line)!=c.mediaRoot{return nil,ErrStrictTransferPlan}
		name:=filepath.Base(line)
		if !inventory[name]||!inventory[name+".json"]||strings.HasSuffix(name,".json"){return nil,ErrStrictTransferPlan}
		delete(inventory,name);delete(inventory,name+".json")
		sidecar,err:=readRegularBounded(line+".json",workerproto.MaxMetadataPostBytes)
		if err!=nil{return nil,ErrStrictTransferPlan}
		var native map[string]json.RawMessage
		if json.Unmarshal(sidecar,&native)!=nil{return nil,ErrStrictTransferPlan}
		var media string
		if json.Unmarshal(native[StrictMediaURLKey],&media)!=nil{return nil,ErrStrictTransferPlan}
		row,err:=json.Marshal([]any{[]any{3,media,json.RawMessage(sidecar)}})
		if err!=nil{return nil,ErrStrictTransferPlan}
		parsed,err:=workerproto.DecodeMetadataEvidence(row,c.request.Metadata,c.request.Subcategory)
		if err!=nil||len(parsed)!=1{return nil,ErrStrictTransferPlan}
		id:=parsed[0].ID
		if seen[id]||approved[id]==nil||!strings.HasPrefix(name,id+"."){return nil,ErrStrictTransferPlan}
		record:=map[string]json.RawMessage{"category":native["category"],"subcategory":native["subcategory"],StrictMediaURLKey:native[StrictMediaURLKey]}
		for k,v:=range native{if StrictEvidenceField(k){record[k]=v}}
		if !sameEvidence(approved[id],record){return nil,ErrStrictTransferPlan}
		size,digest,err:=hashTransferFile(ctx,line,maxTransferTotalBytes-total)
		if err!=nil{return nil,ErrStrictTransferPlan}
		total+=size
		seen[id]=true
		out=append(out,VerifiedTransferFile{ID:id,RelativeName:name,Size:size,SHA256:digest})
	}
	if sc.Err()!=nil||len(inventory)!=0||ctx.Err()!=nil{return nil,ErrStrictTransferPlan}
	return out,nil
}

func sameEvidence(a,b map[string]json.RawMessage)bool{
	if len(a)!=len(b){return false}
	for k,raw:=range a{
		other,ok:=b[k]
		if !ok{return false}
		var x,y any
		d:=json.NewDecoder(bytes.NewReader(raw));d.UseNumber()
		e:=json.NewDecoder(bytes.NewReader(other));e.UseNumber()
		if d.Decode(&x)!=nil||e.Decode(&y)!=nil||!reflect.DeepEqual(x,y){return false}
	}
	return true
}

func openRegular(path string,max int64)(*os.File,os.FileInfo,error){
	info,err:=os.Lstat(path)
	if err!=nil||!info.Mode().IsRegular()||info.Size()<=0||info.Size()>max{return nil,nil,ErrStrictTransferPlan}
	f,err:=os.Open(path)
	if err!=nil{return nil,nil,ErrStrictTransferPlan}
	current,err:=f.Stat()
	if err!=nil||!os.SameFile(info,current)||current.Size()!=info.Size(){_ = f.Close();return nil,nil,ErrStrictTransferPlan}
	return f,info,nil
}
func unchangedRegular(path string,prior os.FileInfo)bool{
	current,err:=os.Lstat(path)
	return err==nil&&current.Mode().IsRegular()&&os.SameFile(prior,current)&&current.Size()==prior.Size()&&current.ModTime()==prior.ModTime()
}
func readRegularBounded(path string,max int64)([]byte,error){
	f,info,err:=openRegular(path,max)
	if err!=nil{return nil,err}
	defer f.Close()
	raw,err:=io.ReadAll(io.LimitReader(f,max+1))
	if err!=nil||int64(len(raw))!=info.Size()||!unchangedRegular(path,info){return nil,ErrStrictTransferPlan}
	return raw,nil
}
func hashTransferFile(ctx context.Context,path string,remaining int64)(int64,string,error){
	limit:=min(maxTransferFileBytes,remaining)
	f,info,err:=openRegular(path,limit)
	if err!=nil{return 0,"",err}
	defer f.Close()
	h:=sha256.New()
	buf:=make([]byte,64<<10)
	var n int64
	for {
		if ctx.Err()!=nil{return 0,"",ErrStrictTransferPlan}
		count,err:=f.Read(buf)
		n+=int64(count)
		if n>limit{return 0,"",ErrStrictTransferPlan}
		_,_=h.Write(buf[:count])
		if err==io.EOF{break}
		if err!=nil{return 0,"",ErrStrictTransferPlan}
	}
	if n!=info.Size()||!unchangedRegular(path,info){return 0,"",ErrStrictTransferPlan}
	return n,hex.EncodeToString(h.Sum(nil)),nil
}
