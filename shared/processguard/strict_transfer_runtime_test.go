//go:build darwin || linux || windows

package processguard

import(
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Actual pinned CLI, extractor, predicate and downloader. Source posts
// are synthetic; only a controller-minted loopback address is reachable.
// This is an application-level socket guard in the test, not a sandbox.
const strictTransferPythonFixture = `
import os
import socket
import sys
from gallery_dl import main
from gallery_dl.extractor import philomena

base = os.environ["MEBOORU_TRANSFER_FIXTURE_BASE"]
port = int(base.rsplit(":", 1)[1])
for name in list(os.environ):
    if name.lower().endswith("_proxy"):
        del os.environ[name]
os.environ["NO_PROXY"] = "*"

connect = socket.socket.connect
connect_ex = socket.socket.connect_ex
getaddrinfo = socket.getaddrinfo
def check(address):
    if not isinstance(address, tuple) or address[:2] != ("127.0.0.1", port):
        raise RuntimeError("only the loopback fixture may be reached")
def guarded_connect(self, address):
    check(address)
    return connect(self, address)
def guarded_connect_ex(self, address):
    check(address)
    return connect_ex(self, address)
def guarded_getaddrinfo(host, service, *args, **kwargs):
    if host != "127.0.0.1" or int(service) != port:
        raise RuntimeError("fixture DNS denied")
    return getaddrinfo(host, service, *args, **kwargs)
socket.socket.connect = guarded_connect
socket.socket.connect_ex = guarded_connect_ex
socket.getaddrinfo = guarded_getaddrinfo

def posts(self):
    listing = [(41, ["safe", "violence"], 41),
               (42, ["safe", "fixture"], 42),
               (43, ["safe", "fixture"], 43),
               (44, ["safe", "fixture"], 44)]
    if "-j" not in sys.argv:
        listing = [(41, ["safe", "violence"], 41),
                   (42, ["safe", "fixture"], 42),
                   (43, ["safe", "violence"], 43),
                   (44, ["safe", "fixture"], 99),
                   (45, ["safe", "fixture"], 45)]
    ids = {"safe": 1, "violence": 2, "fixture": 3}
    for ident, tags, media in listing:
        yield {"id": ident, "tags": tags, "tag_ids": [ids[tag] for tag in tags],
               "created_at": "2020-01-01T00:00:00Z", "format": "jpg",
               "representations": {"full": base + "/" + str(media) + ".jpg"}}
philomena.PhilomenaSearchExtractor.posts = posts
main()
`

func TestStrictTransferOperationPinnedRuntime(t *testing.T){
	python:=os.Getenv("MEBOORU_METADATA_TEST_PYTHON")
	if python==""{t.Skip("requires isolated pinned gallery-dl Python runtime")}
	if !filepath.IsAbs(python){t.Fatal("expected absolute isolated Python path")}
	ctx,cancel:=context.WithTimeout(context.Background(),90*time.Second)
	defer cancel()
	version,err:=exec.CommandContext(ctx,python,"-m","gallery_dl","--version").Output()
	if err!=nil||strings.TrimSpace(string(version))!=acquisitionplan.StrictQualifiedVersion{t.Fatal("wrong pinned runtime")}
	var lock sync.Mutex
	var requests []string
	server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		lock.Lock()
		requests=append(requests,r.Method+" "+r.URL.Path)
		lock.Unlock()
		w.Header().Set("Content-Type","image/jpeg")
		_,_=io.WriteString(w,"synthetic fixture bytes")
	}))
	defer server.Close()
	u,err:=url.Parse(server.URL)
	if err!=nil{t.Fatal(err)}
	host,_,err:=net.SplitHostPort(u.Host)
	if err!=nil||host!="127.0.0.1"{t.Fatal("expected IPv4 loopback fixture")}
	script:=filepath.Join(t.TempDir(),"fixture.py")
	if err:=os.WriteFile(script,[]byte(strictTransferPythonFixture),0600);err!=nil{t.Fatal(err)}
	sum:=sha256.Sum256([]byte(strictTransferPythonFixture))
	digest:=hex.EncodeToString(sum[:])
	set:=NewOperationSet(1)
	defer func(){stop,done:=context.WithTimeout(context.Background(),10*time.Second);defer done();if err:=set.SealAndStop(stop);err!=nil{t.Errorf("cleanup: %v",err)}}()
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	q:=workerproto.MetadataPlanRequest{JobID:1,Gallery:"fixture",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:5}
	r:=acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:digest,ManifestSHA256:digest}
	c:=acquisitionplan.MetadataConfig{Generation:1,Source:q.Source}
	ci,err:=c.Identity()
	if err!=nil{t.Fatal(err)}
	id:=acquisitionplan.MetadataIdentity{Runtime:r,Config:ci}
	start:=func(_ context.Context,args []string)(StreamProbeChild,error){
		raw,err:=json.Marshal(args)
		if err!=nil{return nil,err}
		status:=filepath.Join(t.TempDir(),"status.json")
		reader,writer,err:=os.Pipe()
		if err!=nil{return nil,err}
		defer writer.Close()
		cmd:=fixtureCommand(t,"TestMetadataRuntimeChildFixture","metadata-runtime")
		cmd.Env=append(cmd.Env,"MEBOORU_METADATA_FIXTURE_ARGS="+string(raw),
			"MEBOORU_METADATA_FIXTURE_SCRIPT="+script,"MEBOORU_METADATA_FIXTURE_STATUS="+status,
			"MEBOORU_TRANSFER_FIXTURE_BASE="+server.URL)
		cmd.Stdout=writer
		child,err:=startMetadataRuntimeFixture(cmd)
		if child==nil{_ = reader.Close();return nil,err}
		return &metadataRuntimeChild{OperationChild:child,reader:reader,status:status},err
	}
	metadata,err:=NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},
		func(ctx context.Context,_ workerproto.MetadataKind,args []string)(StreamProbeChild,error){return start(ctx,args)})
	if err!=nil{t.Fatal(err)}
	out,epoch,err:=metadata.RunBound(ctx,q)
	if err!=nil{t.Fatalf("metadata: %v",err)}
	posts,err:=workerproto.DecodeMetadataEvidence(out,q,"search")
	if err!=nil||len(posts)!=4{t.Fatalf("metadata rows: %d %v",len(posts),err)}
	lock.Lock()
	before:=len(requests)
	lock.Unlock()
	if before!=0{t.Fatalf("metadata requested media: %d",before)}
	// Trusted fixture verdict only. Pipeline tests independently exercise
	// the real policy evaluator. This fixture tests changed source evidence
	// and actual HTTP behavior across the owned runtime transfer boundary.
	plan,err:=acquisitionplan.NewStrictTransferPlan(q,"search",posts,[]string{"42","43","44"},1,id,c)
	if err!=nil{t.Fatal(err)}
	root,err:=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	var stages []StrictTransferStage
	files,err:=RunStrictTransfer(ctx,set,plan.Request(),c,root,func(caller context.Context,request workerproto.TransferPlanRequest)(context.Context,[]byte,func(error),error){
		lease,finish,err:=gate.AdmitBound(caller,epoch)
		if err!=nil{return nil,nil,nil,err}
		manifest,err:=plan.Consume(request,id,1)
		if err!=nil{finish(nil);return nil,nil,nil,err}
		return lease,manifest,finish,nil
	},func(context.Context)bool{return true},func(c context.Context,stage StrictTransferStage,args []string)(StreamProbeChild,error){
		stages=append(stages,stage)
		return start(c,args)
	})
	if err!=nil{t.Fatalf("strict transfer stage %v: %v",stages,err)}
	if len(files)!=1||files[0].ID!="42"||files[0].Size!=int64(len("synthetic fixture bytes")){t.Fatalf("verified files: %+v",files)}
	if !slices.Equal(stages,[]StrictTransferStage{TransferVersion,TransferNegative,TransferPositive,TransferMedia}){t.Fatalf("stages: %v",stages)}
	lock.Lock()
	got:=slices.Clone(requests)
	lock.Unlock()
	if !slices.Equal(got,[]string{"GET /42.jpg"}){t.Fatalf("strict media boundary: requests=%v",got)}
	if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
	t.Log("PASS: only unchanged authorized fixture media requested; blocked, changed and newly listed posts made no HTTP requests")
}
