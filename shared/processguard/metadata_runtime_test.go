//go:build darwin || linux || windows

package processguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// Opt-in QA only. The actual pinned Python gallery-dl CLI and its native
// extractors run. Only source posts are substituted. All socket connections
// are refused, so a changed extractor cannot silently access a live site.
const metadataPythonFixture = `
import socket
import sys
from gallery_dl import main
from gallery_dl.extractor import philomena, danbooru, e621

def no_network(*args, **kwargs):
    raise RuntimeError("network disabled for metadata fixture")

socket.socket.connect = no_network
socket.socket.connect_ex = no_network
socket.create_connection = no_network
socket.getaddrinfo = no_network

def philomena_posts(self):
    for ident in (41, 42):
        yield {"id": ident, "tags": ["safe", "fixture"], "tag_ids": [1, 2],
               "created_at": "2020-01-01T00:00:00Z", "format": "jpg",
               "representations": {"full": "https://example.invalid/" + str(ident) + ".jpg"}}

def danbooru_post(self):
    return ({"id": 51, "file_url": "https://example.invalid/51.jpg",
             "created_at": "2020-01-01T00:00:00Z", "rating": "s",
             "tag_string": "fixture", "tag_string_artist": "",
             "tag_string_character": "", "tag_string_copyright": "",
             "tag_string_general": "fixture", "tag_string_meta": ""},)

def e621_post(self):
    return ({"id": 61, "file": {"url": "https://example.invalid/61.jpg",
             "ext": "jpg", "md5": "synthetic"},
             "created_at": "2020-01-01T00:00:00Z", "rating": "s",
             "tags": {"artist": [], "character": [], "copyright": [],
                      "general": ["fixture"], "meta": []}},)

philomena.PhilomenaSearchExtractor.posts = philomena_posts
danbooru.DanbooruPostExtractor.posts = danbooru_post
e621.E621PostExtractor.posts = e621_post
main()
`

type metadataRuntimeStatus struct {
	Exit int
	Stderr []byte
	Failed bool
}

type metadataStderr struct { bytes.Buffer; overflow bool }
func (b *metadataStderr) Write(p []byte)(int,error){
	n:=len(p)
	room:=workerproto.StreamStderrLimit-b.Len()
	if len(p)>room{b.overflow=true;p=p[:room]}
	_,_=b.Buffer.Write(p)
	return n,nil
}

// Trusted test-child entry, reached only after platform activation.
// No production code calls it. Args are locally compiled by the parent.
func runMetadataRuntimeFixture(){
	var args []string
	if json.Unmarshal([]byte(os.Getenv("MEBOORU_METADATA_FIXTURE_ARGS")),&args)!=nil||len(args)==0{os.Exit(2)}
	python:=os.Getenv("MEBOORU_METADATA_TEST_PYTHON")
	script:=os.Getenv("MEBOORU_METADATA_FIXTURE_SCRIPT")
	status:=os.Getenv("MEBOORU_METADATA_FIXTURE_STATUS")
	if !filepath.IsAbs(python)||!filepath.IsAbs(script)||!filepath.IsAbs(status){os.Exit(3)}
	cmd:=exec.Command(python,append([]string{script},args...)...)
	cmd.Dir=filepath.Dir(script)
	cmd.Stdout=os.Stdout
	var stderr metadataStderr
	cmd.Stderr=&stderr
	result:=metadataRuntimeStatus{}
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

type metadataRuntimeChild struct{
	OperationChild
	reader *os.File
	status string
}
func(c *metadataRuntimeChild)ReadChunk(ctx context.Context)([]byte,bool,error){
	type reply struct{b []byte;err error}
	ch:=make(chan reply,1)
	go func(){buf:=make([]byte,workerproto.StreamChunkLimit);n,e:=c.reader.Read(buf);ch<-reply{buf[:n],e}}()
	select{
	case r:=<-ch:if r.err==io.EOF{return r.b,true,nil};return r.b,false,r.err
	case <-ctx.Done():return nil,false,ctx.Err()
	}
}
func(c *metadataRuntimeChild)Result(ctx context.Context)(int,[]byte,error){
	if ctx.Err()!=nil{return 0,nil,ctx.Err()}
	f,err:=os.Open(c.status)
	if err!=nil{return 0,nil,err}
	defer f.Close()
	raw,err:=io.ReadAll(io.LimitReader(f,16<<10))
	var r metadataRuntimeStatus
	if err!=nil||json.Unmarshal(raw,&r)!=nil||r.Failed||len(r.Stderr)>workerproto.StreamStderrLimit{return 0,nil,ErrMetadataOperation}
	return r.Exit,r.Stderr,nil
}
func(c *metadataRuntimeChild)Stop(ctx context.Context)error{
	err:=c.OperationChild.Stop(ctx)
	_ = c.reader.Close()
	return err
}

func TestMetadataOperationPinnedRuntime(t *testing.T){
	python:=os.Getenv("MEBOORU_METADATA_TEST_PYTHON")
	if python==""{t.Skip("requires isolated pinned gallery-dl Python runtime")}
	if !filepath.IsAbs(python){t.Fatal("expected absolute isolated Python path")}
	ctx,cancel:=context.WithTimeout(context.Background(),90*time.Second)
	defer cancel()
	// QA fixture identities only; do not claim installation authentication.
	version,err:=exec.CommandContext(ctx,python,"-m","gallery_dl","--version").Output()
	if err!=nil||strings.TrimSpace(string(version))!=acquisitionplan.StrictQualifiedVersion{t.Fatalf("pinned runtime version: %q %v",version,err)}
	dir:=t.TempDir()
	script:=filepath.Join(dir,"fixture.py")
	if err:=os.WriteFile(script,[]byte(metadataPythonFixture),0600);err!=nil{t.Fatal(err)}
	sum:=sha256.Sum256([]byte(metadataPythonFixture))
	digest:=hex.EncodeToString(sum[:])
	for _,tc:=range []struct{source,url,id string; last uint32}{
		{"derpibooru","https://derpibooru.org/search?q=fixture","41",2},
		{"danbooru","https://danbooru.donmai.us/posts/51","51",1},
		{"e621","https://e621.net/posts/61","61",1},
	}{
		t.Run(tc.source,func(t *testing.T){
			set:=NewOperationSet(1)
			defer func(){stop,done:=context.WithTimeout(context.Background(),10*time.Second);defer done();if err:=set.SealAndStop(stop);err!=nil{t.Errorf("cleanup: %v",err)}}()
			gate:=workerproto.NewDrainGate(ctx)
			if err:=gate.Open();err!=nil{t.Fatal(err)}
			q:=workerproto.MetadataPlanRequest{JobID:1,Gallery:"fixture",Kind:workerproto.MetadataStrictResolve,Source:tc.source,URL:tc.url,First:1,Last:tc.last}
			r:=acquisitionplan.RuntimeIdentity{Generation:1,Version:acquisitionplan.StrictQualifiedVersion,ArtifactSHA256:digest,ManifestSHA256:digest}
			c:=acquisitionplan.MetadataConfig{Generation:1,Source:tc.source}
			ci,err:=c.Identity()
			if err!=nil{t.Fatal(err)}
			id:=acquisitionplan.MetadataIdentity{Runtime:r,Config:ci}
			starts:=0
			o,err:=NewMetadataOperation(set,gate,q,r,c,func(context.Context)(acquisitionplan.MetadataIdentity,error){return id,nil},func(_ context.Context,kind workerproto.MetadataKind,args []string)(StreamProbeChild,error){
				starts++
				raw,err:=json.Marshal(args)
				if err!=nil{return nil,err}
				status:=filepath.Join(t.TempDir(),"status.json")
				reader,writer,err:=os.Pipe()
				if err!=nil{return nil,err}
				defer writer.Close()
				cmd:=fixtureCommand(t,"TestMetadataRuntimeChildFixture","metadata-runtime")
				cmd.Env=append(cmd.Env,"MEBOORU_METADATA_FIXTURE_ARGS="+string(raw),"MEBOORU_METADATA_FIXTURE_SCRIPT="+script,"MEBOORU_METADATA_FIXTURE_STATUS="+status)
				cmd.Stdout=writer
				child,err:=startMetadataRuntimeFixture(cmd)
				if child==nil{_ = reader.Close();return nil,err}
				return &metadataRuntimeChild{OperationChild:child,reader:reader,status:status},err
			})
			if err!=nil{t.Fatal(err)}
			out,err:=o.Run(ctx,q)
			if err!=nil||starts!=2{t.Fatalf("real contained metadata refused: %v (starts=%d)",err,starts)}
			var messages [][]json.RawMessage
			if err:=json.Unmarshal(out,&messages);err!=nil{t.Fatal(err)}
			count:=0
			for _,msg:=range messages{
				if len(msg)<1{t.Fatal("empty message")}
				if string(msg[0])!="3"{continue}
				if len(msg)!=3{t.Fatal("unexpected file message")}
				var meta map[string]json.RawMessage
				if err:=json.Unmarshal(msg[2],&meta);err!=nil{t.Fatal(err)}
				var category string
				if json.Unmarshal(meta["category"],&category)!=nil||category!=tc.source||len(meta["tags"])==0{t.Fatalf("missing native metadata for %s",tc.source)}
				if count==0&&string(meta["id"])!=tc.id{t.Fatalf("unexpected ID: %s",meta["id"])}
				count++
			}
			if count!=int(tc.last){t.Fatalf("post count: got %d want %d",count,tc.last)}
			if err:=gate.Drain(ctx);err!=nil{t.Fatal(err)}
		})
	}
}
