package acquisitionplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// StrictGateExpression is the existing qualified predicate shared by
// legacy and managed paths. Only a trusted local path is interpolated.
func StrictGateExpression(path string) string {
	return "(lambda d, j, p: (lambda r: r is not None and " +
		"all(k in d and (list(d[k]) if isinstance(d[k], tuple) else d[k]) == v for k,v in r.items()) and " +
		"all(k in r for k in d if k in ('tags','tag_ids','rating') or k.startswith('tags_')))(" +
		"(j.__dict__.get(p) or j.__dict__.setdefault(p, j.loads(std.pathlib.Path(p).read_text(encoding='utf-8')))).get(str(d.get('id'))))" +
		")(locals(), std.json, " + strconv.QuoteToASCII(path) + ")"
}


// TransferCommands is a local candidate, never a wire DTO. The owner
// must hold the original admission lease, verify current policy/runtime,
// and run the version and both predicate probes with cleanup proof
// before activating Transfer. No archive or custom configuration yet.
type TransferCommands struct {
	Version []string
	Negative []string
	Positive []string
	Transfer []string
	manifestPath string
	probePath string
	manifest []byte
	request workerproto.TransferPlanRequest
	mediaRoot string
}

// PrepareTransferCommands accepts only a fresh, empty, owner-created
// disposable QA directory. It deliberately does not implement production
// rooted staging or protect against a hostile same-user file replacement.
func PrepareTransferCommands(q workerproto.TransferPlanRequest,manifest []byte,config MetadataConfig,root string)(TransferCommands,error){
	if !workerproto.ValidTransferPlan(q)||config.Source!=q.Metadata.Source||
		!filepath.IsAbs(root)||filepath.Clean(root)!=root||len(manifest)==0||len(manifest)>workerproto.MaxMetadataOutputBytes{
		return TransferCommands{},ErrStrictTransferPlan
	}
	sum:=sha256.Sum256(manifest)
	if hex.EncodeToString(sum[:])!=q.ManifestSHA256{return TransferCommands{},ErrStrictTransferPlan}
	options,err:=config.args()
	if err!=nil{return TransferCommands{},ErrStrictTransferPlan}
	actual,err:=filepath.EvalSymlinks(root)
	if err!=nil||actual!=root{return TransferCommands{},ErrStrictTransferPlan}
	entries,err:=os.ReadDir(root)
	if err!=nil||len(entries)!=0{return TransferCommands{},ErrStrictTransferPlan}
	var records map[string]map[string]json.RawMessage
	if json.Unmarshal(manifest,&records)!=nil||len(records)==0||len(records)>int(q.Metadata.Last-q.Metadata.First+1){
		return TransferCommands{},ErrStrictTransferPlan
	}
	manifestPath:=filepath.Join(root,"strict-blacklist.json")
	probePath:=filepath.Join(root,"strict-blacklist-probe.json")
	dest:=filepath.Join(root,"media")
	if err:=writeExclusive(manifestPath,manifest);err!=nil{return TransferCommands{},ErrStrictTransferPlan}
	if err:=writeExclusive(probePath,[]byte(probeManifest));err!=nil{return TransferCommands{},ErrStrictTransferPlan}
	if err:=os.Mkdir(dest,0700);err!=nil{return TransferCommands{},ErrStrictTransferPlan}
	return compileTransferCommands(q,manifest,options,root),nil
}

// ObservePreparedTransferCommands reconstructs the exact locally
// compiled commands without creating or modifying any workspace file.
// Custody must supply the owner-authorized allocation and canonical root.
func ObservePreparedTransferCommands(q workerproto.TransferPlanRequest,config MetadataConfig,root string)(TransferCommands,error){
	if !workerproto.ValidTransferPlan(q)||config.Source!=q.Metadata.Source||!filepath.IsAbs(root)||filepath.Clean(root)!=root{return TransferCommands{},ErrStrictTransferPlan}
	actual,err:=filepath.EvalSymlinks(root);if err!=nil||actual!=root{return TransferCommands{},ErrStrictTransferPlan}
	options,err:=config.args();if err!=nil{return TransferCommands{},err}
	path:=filepath.Join(root,"strict-blacklist.json")
	info,err:=os.Lstat(path);if err!=nil||!info.Mode().IsRegular()||info.Size()<1||info.Size()>workerproto.MaxMetadataOutputBytes{return TransferCommands{},ErrStrictTransferPlan}
	manifest,err:=os.ReadFile(path);if err!=nil||int64(len(manifest))!=info.Size(){return TransferCommands{},ErrStrictTransferPlan}
	if _,err:=NewStrictManifestVerifier(q,manifest);err!=nil{return TransferCommands{},err}
	c:=compileTransferCommands(q,manifest,options,root)
	if !c.VerifyFiles(){return TransferCommands{},ErrStrictTransferPlan}
	media,err:=os.Lstat(filepath.Join(root,"media"));if err!=nil||!media.IsDir(){return TransferCommands{},ErrStrictTransferPlan}
	return c,nil
}
func compileTransferCommands(q workerproto.TransferPlanRequest,manifest []byte,options []string,root string)TransferCommands{
	manifestPath:=filepath.Join(root,"strict-blacklist.json")
	probePath:=filepath.Join(root,"strict-blacklist-probe.json")
	dest:=filepath.Join(root,"media")
	gate:=StrictGateExpression(manifestPath)
	probe:=func(filter string)[]string{
		return []string{"--config-ignore","-j","-o",`keywords-global={"id":1,"tags":["probe"]}`,"--filter",filter,"--","https://example.invalid/meloader-filter-probe.jpg"}
	}
	args:=append([]string{"--config-ignore"},options...)
	args=append(args,StrictSourceArgs()...)
	args=append(args,"-D",dest,"--filter",gate,"-o","fallback=false","-o","archive=",
		"-o",`postprocessors=[{"name":"metadata","mode":"json","event":"file"}]`,"-o","filename={id}.{extension}",
		"--post-range",strconv.FormatUint(uint64(q.Metadata.First),10)+"-"+strconv.FormatUint(uint64(q.Metadata.Last),10),"--",q.Metadata.URL)
	return TransferCommands{Version:[]string{"--config-ignore","--version"},Negative:probe(gate),Positive:probe(StrictGateExpression(probePath)),Transfer:args,
		manifestPath:manifestPath,probePath:probePath,manifest:bytes.Clone(manifest),request:q,mediaRoot:dest}
}
const probeManifest = `{"1":{"category":"directlink","tags":["probe"]}}`

func writeExclusive(path string,body []byte)error{
	f,err:=os.OpenFile(path,os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600)
	if err!=nil{return err}
	_,err=f.Write(body)
	closeErr:=f.Close()
	if err!=nil{return err}
	return closeErr
}

// VerifyFiles is a point-in-time check of the two local manifest files,
// not an atomic launch or an OS sandbox guarantee.
func(c TransferCommands)VerifyFiles()bool{
	for _,item:=range []struct{path string; want []byte}{{c.manifestPath,c.manifest},{c.probePath,[]byte(probeManifest)}}{
		info,err:=os.Lstat(item.path)
		if err!=nil||!info.Mode().IsRegular()||info.Size()!=int64(len(item.want)){return false}
		body,err:=os.ReadFile(item.path)
		if err!=nil||!bytes.Equal(body,item.want){return false}
	}
	return true
}

// ValidatePredicateProbe verifies exactly one directory and the expected
// file count. Failure or warning must be rejected by the owned executor.
func ValidatePredicateProbe(body []byte,positive bool)bool{
	var messages [][]json.RawMessage
	want:=1
	if positive{want++}
	return json.Unmarshal(body,&messages)==nil&&len(messages)==want&&len(messages[0])==2&&string(messages[0][0])=="2"&&
		(!positive||len(messages[1])==3&&string(messages[1][0])=="3")
}
