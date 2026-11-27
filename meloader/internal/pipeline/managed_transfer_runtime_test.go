//go:build darwin || linux || windows

package pipeline

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
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/mapping"
	"bytes"
	"errors"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Actual pinned CLI, extractor, predicate and downloader. Source posts
// are synthetic; only a controller-minted loopback address is reachable.
// This is an application-level socket guard in the test, not a sandbox.
const composedStrictPythonFixture = `
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
    listing = [(41, ["safe", "dashie", "violence"], 41),
               (42, ["explicit", "dashie"], 42),
               (43, ["safe", "dashie", "fixture"], 43),
               (44, ["safe", "dashie", "fixture"], 44)]
    if "-j" not in sys.argv:
        listing = [(41, ["safe", "dashie", "violence"], 41),
                   (42, ["explicit", "dashie"], 42),
                   (43, ["safe", "dashie", "violence"], 43),
                   (44, ["safe", "dashie", "fixture"], 99),
                   (45, ["safe", "dashie", "fixture"], 45)]
    ids = {"safe": 1, "explicit": 2, "violence": 3, "fixture": 4, "dashie": 24568}
    for ident, tags, media in listing:
        yield {"id": ident, "tags": tags, "tag_ids": [ids[tag] for tag in tags],
               "created_at": "2020-01-01T00:00:00Z", "format": "jpg",
               "representations": {"full": base + "/" + str(media) + ".jpg"}}
philomena.PhilomenaSearchExtractor.posts = posts
main()
`

func TestManagedStrictTransferPinnedRuntime(t *testing.T){
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
	if err:=os.WriteFile(script,[]byte(composedStrictPythonFixture),0600);err!=nil{t.Fatal(err)}
	sum:=sha256.Sum256([]byte(composedStrictPythonFixture))
	digest:=hex.EncodeToString(sum[:])
	set:=processguard.NewOperationSet(1)
	defer func(){stop,done:=context.WithTimeout(context.Background(),10*time.Second);defer done();if err:=set.SealAndStop(stop);err!=nil{t.Errorf("cleanup: %v",err)}}()
	gate:=workerproto.NewDrainGate(ctx)
	if err:=gate.Open();err!=nil{t.Fatal(err)}
	q:=workerproto.MetadataPlanRequest{JobID:1,Gallery:"fixture",Kind:workerproto.MetadataStrictResolve,Source:"derpibooru",URL:"https://derpibooru.org/search?q=fixture",First:1,Last:5}
	r:=acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:digest,ManifestSHA256:digest}
	c:=acquisitionplan.MetadataConfig{Generation:1,Source:q.Source}
	ci,err:=c.Identity()
	if err!=nil{t.Fatal(err)}
	id:=acquisitionplan.MetadataIdentity{Runtime:r,Config:ci}
	start:=func(_ context.Context,args []string)(processguard.StreamProbeChild,error){
		raw,err:=json.Marshal(args)
		if err!=nil{return nil,err}
		status:=filepath.Join(t.TempDir(),"status.json")
		reader,writer,err:=os.Pipe()
		if err!=nil{return nil,err}
		defer writer.Close()
		cmd:=composedRuntimeCommand(t,"runtime")
		cmd.Env=append(cmd.Env,"MEBOORU_METADATA_FIXTURE_ARGS="+string(raw),
			"MEBOORU_METADATA_FIXTURE_SCRIPT="+script,"MEBOORU_METADATA_FIXTURE_STATUS="+status,
			"MEBOORU_TRANSFER_FIXTURE_BASE="+server.URL)
		cmd.Stdout=writer
		child,err:=startComposedRuntimeFixture(cmd)
		if child==nil{_ = reader.Close();return nil,err}
		return &composedRuntimeChild{OperationChild:child,reader:reader,status:status},err
	}
	metadata,err:=processguard.NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},
		func(ctx context.Context,_ workerproto.MetadataKind,args []string)(processguard.StreamProbeChild,error){return start(ctx,args)})
	if err!=nil{t.Fatal(err)}
	completion,err:=metadata.RunCompletion(ctx,q)
	if err!=nil{t.Fatalf("metadata: %v",err)}
	lock.Lock()
	before:=len(requests)
	lock.Unlock()
	if before!=0{t.Fatalf("metadata requested media: %d",before)}
	// Actual policy evaluator and reciprocal alias verification. Only
	// source responses are substituted; no precomputed approved subset.
	cfg:=config.Default()
	cfg.Tags.StrictBlacklist=true
	cfg.Tags.Blacklist=[]string{"character:rainbow_dash violence"}
	mapper,err:=mapping.New(config.NewProvider(cfg),"")
	if err!=nil{t.Fatal(err)}
	transport:=managedEvidenceTransport(func(req *http.Request)(*http.Response,error){
		var data string
		switch req.URL.Path{
		case "/api/v1/json/search/tags":
			data=`{"tags":[{"id":1,"name":"safe","category":"rating","aliased_tag":null,"aliases":[]},{"id":2,"name":"explicit","category":"rating","aliased_tag":null,"aliases":[]},{"id":3,"name":"violence","category":null,"aliased_tag":null,"aliases":[]},{"id":4,"name":"fixture","category":null,"aliased_tag":null,"aliases":[]},{"id":24568,"name":"dashie","slug":"dashie","category":null,"aliased_tag":"rainbow+dash","aliases":[]}]}`
		case "/api/v1/json/tags/rainbow+dash":
			data=`{"tag":{"id":39229,"name":"rainbow dash","slug":"rainbow+dash","category":"character","aliased_tag":null,"aliases":["dashie"]}}`
		default:t.Fatal("unexpected taxonomy endpoint")
		}
		return &http.Response{StatusCode:200,Header:make(http.Header),Body:io.NopCloser(strings.NewReader(data))},nil
	})
	p,err:=NewManagedPreflightFromCompletion(ctx,completion,q,id,c,gate,func(_ context.Context,req workerproto.MetadataPlanRequest)bool{return req==q},mapper,transport,1,func(context.Context)(acquisitionplan.MetadataIdentity,uint64,error){return id,1,nil})
	if err!=nil{t.Fatal(err)}
	result,err:=p.Run(ctx,q)
	if err!=nil||result.Approval==nil||!slices.Equal(result.BlockedIDs,[]string{"41"}){t.Fatalf("preflight: %+v %v",result,err)}
	lock.Lock()
	before=len(requests)
	lock.Unlock()
	if before!=0{t.Fatalf("preflight requested media: %d",before)}
	root,err:=filepath.EvalSymlinks(t.TempDir())
	if err!=nil{t.Fatal(err)}
	var stages []processguard.StrictTransferStage
	files,err:=result.Approval.runTransferCandidate(ctx,result.Approval.Request(),set,root,func(c context.Context,stage processguard.StrictTransferStage,args []string)(processguard.StreamProbeChild,error){
		stages=append(stages,stage)
		return start(c,args)
	})
	if err!=nil{t.Fatalf("strict transfer stage %v: %v",stages,err)}
	expectedHash:=sha256.Sum256([]byte("synthetic fixture bytes"))
	if len(files)!=1||files[0].ID!="42"||files[0].Size!=int64(len("synthetic fixture bytes"))||files[0].SHA256!=hex.EncodeToString(expectedHash[:]){t.Fatalf("verified files: %+v",files)}
	if !slices.Equal(stages,[]processguard.StrictTransferStage{processguard.TransferVersion,processguard.TransferNegative,processguard.TransferPositive,processguard.TransferMedia}){t.Fatalf("stages: %v",stages)}
	lock.Lock()
	got:=slices.Clone(requests)
	lock.Unlock()
	if !slices.Equal(got,[]string{"GET /42.jpg"}){t.Fatalf("strict media boundary: requests=%v",got)}
	if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
	t.Log("PASS: actual alias-aware strict preflight, contained runtime and verified file; only approved unchanged media requested")
}

// Test-only runtime child adapter; no application binary includes this entry.
func composedRuntimeCommand(t *testing.T, mode string)*exec.Cmd{
	t.Helper()
	exe,err:=os.Executable()
	if err!=nil{t.Fatal(err)}
	cmd:=exec.Command(exe,"-test.run=^TestComposedRuntimeChildFixture$")
	// Remove inherited mode to make child/grandchild selection exact.
	for _,v:=range os.Environ(){if !strings.HasPrefix(v,"MEBOORU_COMPOSED_FIXTURE="){cmd.Env=append(cmd.Env,v)}}
	cmd.Env=append(cmd.Env,"MEBOORU_COMPOSED_FIXTURE="+mode)
	cmd.Stderr=os.Stderr
	return cmd
}

type composedRuntimeStatus struct {
	Exit int
	Stderr []byte
	Failed bool
}

type composedStderr struct { bytes.Buffer; overflow bool }
func (b *composedStderr) Write(p []byte)(int,error){
	n:=len(p)
	room:=workerproto.StreamStderrLimit-b.Len()
	if len(p)>room{b.overflow=true;p=p[:room]}
	_,_=b.Buffer.Write(p)
	return n,nil
}

// Trusted test-child entry, reached only after platform activation.
// No production code calls it. Args are locally compiled by the parent.
func runComposedRuntimeFixture(){
	var args []string
	if json.Unmarshal([]byte(os.Getenv("MEBOORU_METADATA_FIXTURE_ARGS")),&args)!=nil||len(args)==0{os.Exit(2)}
	python:=os.Getenv("MEBOORU_METADATA_TEST_PYTHON")
	script:=os.Getenv("MEBOORU_METADATA_FIXTURE_SCRIPT")
	status:=os.Getenv("MEBOORU_METADATA_FIXTURE_STATUS")
	if !filepath.IsAbs(python)||!filepath.IsAbs(script)||!filepath.IsAbs(status){os.Exit(3)}
	cmd:=exec.Command(python,append([]string{script},args...)...)
	cmd.Dir=filepath.Dir(script)
	cmd.Stdout=os.Stdout
	var stderr composedStderr
	cmd.Stderr=&stderr
	result:=composedRuntimeStatus{}
	if err:=cmd.Run();err!=nil{
		var exit *exec.ExitError
		if errors.As(err,&exit){result.Exit=exit.ExitCode()}else{result.Failed=true}
	}
	result.Stderr=stderr.Bytes()
	result.Failed=result.Failed||stderr.overflow
	raw,err:=json.Marshal(result)
	if err!=nil||os.WriteFile(status,raw,0600)!=nil{os.Exit(4)}
	os.Exit(0)
}

type composedRuntimeChild struct{
	processguard.OperationChild
	reader *os.File
	status string
}
func(c *composedRuntimeChild)ReadChunk(ctx context.Context)([]byte,bool,error){
	type reply struct{b []byte;err error}
	ch:=make(chan reply,1)
	go func(){buf:=make([]byte,workerproto.StreamChunkLimit);n,e:=c.reader.Read(buf);ch<-reply{buf[:n],e}}()
	select{
	case r:=<-ch:if r.err==io.EOF{return r.b,true,nil};return r.b,false,r.err
	case <-ctx.Done():return nil,false,ctx.Err()
	}
}
func(c *composedRuntimeChild)Result(ctx context.Context)(int,[]byte,error){
	if ctx.Err()!=nil{return 0,nil,ctx.Err()}
	f,err:=os.Open(c.status)
	if err!=nil{return 0,nil,err}
	defer f.Close()
	raw,err:=io.ReadAll(io.LimitReader(f,16<<10))
	var r composedRuntimeStatus
	if err!=nil||json.Unmarshal(raw,&r)!=nil||r.Failed||len(r.Stderr)>workerproto.StreamStderrLimit{return 0,nil,ErrManagedPreflight}
	return r.Exit,r.Stderr,nil
}
func(c *composedRuntimeChild)Stop(ctx context.Context)error{
	err:=c.OperationChild.Stop(ctx)
	_ = c.reader.Close()
	return err
}

