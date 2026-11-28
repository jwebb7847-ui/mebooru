// Package managedqa contains a fixed, loopback-only disposable application
// fixture. It is never imported by production acquisition authority.
package managedqa

import(
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

const Prefix="mebooru-managed-probe-"
const Search="https://derpibooru.org/search?q=fixture"
const Gallery="A"
const SetupName="window-setup.json"
const MetadataName="window-native.json"
const GrantName="window-grant.json"
const ProofName="window-proof.json"
const RevokedName="window-revoked"
const ScriptName="window-runtime.py"
var ErrFixture=errors.New("disposable managed window refused")

type Setup struct{
	Count int
	Windows int
	Port int
	Python string
	ScriptSHA256 string
	Application bool `json:",omitempty"`
	Destination string `json:",omitempty"`
	CancelFirst bool `json:",omitempty"`
}
func Digest(b []byte)string{v:=sha256.Sum256(b);return hex.EncodeToString(v[:])}
func ValidateDir(dir string)error{
	temp,err:=filepath.EvalSymlinks(os.TempDir())
	if err!=nil||!filepath.IsAbs(dir)||filepath.Dir(dir)!=temp||!strings.HasPrefix(filepath.Base(dir),Prefix){return ErrFixture}
	i,err:=os.Lstat(dir);if err!=nil||!i.IsDir(){return ErrFixture}
	return nil
}
func Read(dir,name string,limit int)([]byte,error){
	if ValidateDir(dir)!=nil||filepath.Base(name)!=name{return nil,ErrFixture}
	root,err:=os.OpenRoot(dir);if err!=nil{return nil,err};defer root.Close()
	i,err:=root.Lstat(name);if err!=nil||!i.Mode().IsRegular()||i.Size()<1||i.Size()>int64(limit){return nil,ErrFixture}
	f,err:=root.Open(name);if err!=nil{return nil,err};defer f.Close()
	actual,err:=f.Stat();if err!=nil||!os.SameFile(i,actual){return nil,ErrFixture}
	b,err:=io.ReadAll(io.LimitReader(f,int64(limit)+1));if err!=nil||int64(len(b))!=i.Size(){return nil,ErrFixture};return b,nil
}
func Write(dir,name string,b []byte)error{
	if ValidateDir(dir)!=nil||filepath.Base(name)!=name||len(b)==0||len(b)>workerproto.MaxMetadataOutputBytes{return ErrFixture}
	root,err:=os.OpenRoot(dir);if err!=nil{return err};defer root.Close()
	f,err:=root.OpenFile(name,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if err!=nil{return err}
	n,err:=f.Write(b);if err==nil&&n!=len(b){err=io.ErrShortWrite}
	if err==nil{err=f.Sync()};return errors.Join(err,f.Close())
}
func WriteJSON(dir,name string,v any)error{
	b,err:=json.Marshal(v);if err!=nil{return err};return Write(dir,name,b)
}
func ReadJSON(dir,name string,v any,limit int)error{
	b,err:=Read(dir,name,limit);if err!=nil{return err}
	if json.Unmarshal(b,v)!=nil{return ErrFixture}
	c,err:=json.Marshal(v);if err!=nil||!bytes.Equal(c,b){return ErrFixture};return nil
}
func Load(dir string)(Setup,error){
	var s Setup
	if err:=ReadJSON(dir,SetupName,&s,8192);err!=nil{return s,err}
	if !ValidCount(s.Count,s.Application)||(s.Windows<1||s.Windows>3)||s.Windows>1&&s.Count!=3||s.Port<1||s.Port>65535||!filepath.IsAbs(s.Python)||s.ScriptSHA256!=Digest([]byte(PythonFixture)){return Setup{},ErrFixture}
	if s.Destination!=""&&(!s.Application||!workerproto.ValidMetadataPlan(s.MetadataFor(1))){return Setup{},ErrFixture}
	if s.CancelFirst&&(!s.Application||s.Count!=3||s.Windows!=2){return Setup{},ErrFixture}
	b,err:=Read(dir,ScriptName,1<<20);if err!=nil||Digest(b)!=s.ScriptSHA256{return Setup{},ErrFixture}
	return s,nil
}
func ValidCount(count int, application bool) bool {
	return count == 3 || count == 200 || application && (count == 1 || count == 20)
}
func(s Setup) Metadata()workerproto.MetadataPlanRequest{return s.MetadataFor(1)}
func(s Setup) MetadataFor(job int64)workerproto.MetadataPlanRequest{
	if job<1||job>int64(s.Windows){return workerproto.MetadataPlanRequest{}}
	url:=Search;if job>1{url+=strconv.FormatInt(job,10)}
	gallery := s.Destination
	if gallery == "" { gallery = Gallery }
	return workerproto.MetadataPlanRequest{JobID:job,Gallery:gallery,Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:url,First:1,Last:uint32(s.Count)}
}
func JobFile(name string,job int64)string{return strconv.FormatInt(job,10)+"-"+name}
func ParentID(job int64)string{return fmt.Sprintf("%032x",job)}
func(s Setup) Offset(job int64)int{return int(job-1)*s.Count}
func(s Setup) Identity()(acquisitionplan.RuntimeIdentity,acquisitionplan.MetadataConfig,acquisitionplan.MetadataIdentity){
	r:=acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:s.ScriptSHA256,ManifestSHA256:s.ScriptSHA256}
	c:=acquisitionplan.MetadataConfig{Generation:1,Source:"derpibooru"}
	id,_:=c.Identity()
	return r,c,acquisitionplan.MetadataIdentity{Runtime:r,Config:id}
}
func(s Setup) MediaURL(id string)string{return fmt.Sprintf("http://127.0.0.1:%d/%s.png",s.Port,id)}
func(s Setup) AllBlocked(job int64)bool{return s.Windows==3&&job==2}
func(s Setup) Blocked(job int64, index int) bool { return s.AllBlocked(job) || s.Count > 1 && index == 0 }
func(s Setup) ExpectedIDs(job int64)[]string{
	out:=make([]string,0,s.Count)
	for i:=0;i<s.Count;i++{
		if !s.Blocked(job,i){out=append(out,strconv.Itoa(s.Offset(job)+i+1))}
	}
	return out
}
func Workspace(dir string,g workerproto.StageAllocation)(string,error){
	if ValidateDir(dir)!=nil||!workerproto.ValidStageAllocation(g){return "",ErrFixture}
	p:=filepath.Join(dir,"stage",g.Request.OperationID,g.TransferID)
	actual,err:=filepath.EvalSymlinks(p);if err!=nil||actual!=p{return "",ErrFixture}
	i,err:=os.Lstat(p);if err!=nil||!i.IsDir(){return "",ErrFixture};return p,nil
}
type Grant struct{
	Allocation workerproto.StageAllocation
	PolicySHA256 string
}
type Proof struct{
	Allocation workerproto.StageAllocation
	Files []acquisitionplan.VerifiedTransferFile
}

// Actual pinned gallery-dl CLI, replacing only source responses. The
// fixture refuses every network socket except the controller's IPv4
// loopback server; not a general OS sandbox or production trust record.
const PythonFixture = `
import os, socket, sys, time
from gallery_dl import main
from gallery_dl.extractor import philomena
port = int(os.environ["MEBOORU_WINDOW_PORT"])
count = int(os.environ["MEBOORU_WINDOW_COUNT"])
windows = int(os.environ["MEBOORU_WINDOW_WINDOWS"])
base = "http://127.0.0.1:" + str(port)
for name in list(os.environ):
    if name.lower().endswith("_proxy"):
        del os.environ[name]
os.environ["NO_PROXY"] = "*"
connect, connect_ex, getaddrinfo = socket.socket.connect, socket.socket.connect_ex, socket.getaddrinfo
def check(address):
    if not isinstance(address, tuple) or address[:2] != ("127.0.0.1", port):
        raise RuntimeError("fixture socket refused")
def checked_connect(self, address):
    check(address)
    return connect(self, address)
def checked_connect_ex(self, address):
    check(address)
    return connect_ex(self, address)
def checked_lookup(host, service, *a, **k):
    if host != "127.0.0.1" or int(service) != port:
        raise RuntimeError("fixture DNS refused")
    return getaddrinfo(host, service, *a, **k)
socket.socket.connect, socket.socket.connect_ex, socket.getaddrinfo = checked_connect, checked_connect_ex, checked_lookup
def posts(self):
    job = 3 if sys.argv[-1].endswith("q=fixture3") else 2 if sys.argv[-1].endswith("q=fixture2") else 1
    if job == 1 and os.environ.get("MEBOORU_WINDOW_CANCEL_FIRST") == "true":
        with open("cancel-fixture-ready", "x"):
            pass
        time.sleep(30)
    offset = (job - 1) * count
    for ident in range(offset + 1, offset + count + 1):
        blocked = (count > 1 and ident == offset + 1) or (windows == 3 and job == 2)
        tags = ["safe", "violence"] if blocked else ["safe", "fixture"]
        ids = [1, 3] if blocked else [1, 4]
        yield {"id": ident, "tags": tags, "tag_ids": ids,
               "created_at": "2020-01-01T00:00:00Z", "format": "png",
               "representations": {"full": base + "/" + str(ident) + ".png"}}
philomena.PhilomenaSearchExtractor.posts = posts
main()
`
