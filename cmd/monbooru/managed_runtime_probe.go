package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

const runtimeProbeOutputLimit=4096
const runtimeProbeResultLimit=16<<10
const runtimeProbeBinaryLimit=512<<20
var errRuntimeProbe=errors.New("managed runtime version probe refused")
var runtimeVersionPattern=regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}$`)

func runtimeProbeRoot()(string,error){
	self,err:=os.Executable()
	if err!=nil{return "",err}
	self,err=filepath.EvalSymlinks(self)
	if err!=nil{return "",err}
	name:="monbooru"
	if runtime.GOOS=="windows"{name+=".exe"}
	if filepath.Base(self)!=name{return "",errRuntimeProbe}
	return filepath.Dir(self),nil
}

// Diagnostic only: expected digest supplied by the isolated QA fixture,
// NOT a signature/production release trust record. No caller-controlled
// executable name or runtime path; no PATH lookup, symlink or shell.
// Hashing and path verification do not defend against same-user swaps,
// nor does a script hash identify its interpreter and dependencies.
func verifyRuntimeProbeBinary(root,digest string)(string,error){
	raw,err:=hex.DecodeString(digest)
	if err!=nil||len(raw)!=sha256.Size||hex.EncodeToString(raw)!=digest{return "",errRuntimeProbe}
	name:="gallery-dl"
	if runtime.GOOS=="windows"{name+=".exe"}
	path:=filepath.Join(root,name)
	info,err:=os.Lstat(path)
	if err!=nil{return "",err}
	if !info.Mode().IsRegular()||info.Size()<=0||info.Size()>runtimeProbeBinaryLimit||(runtime.GOOS!="windows"&&info.Mode().Perm()&0111==0){return "",errRuntimeProbe}
	resolved,err:=filepath.EvalSymlinks(path)
	if err!=nil||resolved!=path{return "",errRuntimeProbe}
	f,err:=os.Open(path)
	if err!=nil{return "",err}
	defer f.Close()
	opened,err:=f.Stat()
	if err!=nil||!os.SameFile(info,opened)||!opened.Mode().IsRegular(){return "",errRuntimeProbe}
	h:=sha256.New()
	n,err:=io.Copy(h,io.LimitReader(f,runtimeProbeBinaryLimit+1))
	if err!=nil||n!=info.Size()||!bytes.Equal(h.Sum(nil),raw){return "",errRuntimeProbe}
	return path,nil
}
func readRuntimeProbeDigest(root string)(string,error){
	path:=filepath.Join(root,"gallery-dl.probe.sha256")
	info,err:=os.Lstat(path)
	if err!=nil{return "",err}
	if !info.Mode().IsRegular()||info.Size()!=65{return "",errRuntimeProbe}
	f,err:=os.Open(path)
	if err!=nil{return "",err}
	defer f.Close()
	opened,err:=f.Stat()
	if err!=nil||!os.SameFile(info,opened){return "",errRuntimeProbe}
	body,err:=io.ReadAll(io.LimitReader(f,66))
	if err!=nil||len(body)!=65||body[64]!='\n'{return "",errRuntimeProbe}
	return string(body[:64]),nil
}

type probeOutput struct{buffer bytes.Buffer;overflow bool}
func(p *probeOutput)Bytes()[]byte{return p.buffer.Bytes()}
func(p *probeOutput)Write(b []byte)(int,error){
	// Continue draining after overflow, but never accept a truncated
	// successful output. Custodian deadline still bounds execution.
	n:=len(b)
	room:=runtimeProbeOutputLimit-p.buffer.Len()
	if len(b)>room{p.overflow=true;b=b[:room]}
	_,_=p.buffer.Write(b)
	return n,nil
}

type runtimeVersionResult struct{
	Started bool `json:"started"`
	ExitCode int `json:"exit_code"`
	Failed bool `json:"failed"`
	Overflow bool `json:"overflow"`
	Stdout []byte `json:"stdout"`
	Stderr []byte `json:"stderr"`
}
func(r runtimeVersionResult)version()(string,error){
	v:=strings.TrimSpace(string(r.Stdout))
	if !r.Started||r.Failed||r.Overflow||r.ExitCode!=0||len(r.Stdout)>runtimeProbeOutputLimit||len(r.Stderr)!=0||!runtimeVersionPattern.MatchString(v){return "",errRuntimeProbe}
	return v,nil
}

// Called only after platform activation and guard watcher installation.
// The wrapper inherits the owned group/Job and starts the fixed child
// without creating any new containment boundary. No general argv input.
func runRuntimeVersionLeaf(digest string)error{
	root,err:=runtimeProbeRoot()
	if err!=nil{return err}
	path,err:=verifyRuntimeProbeBinary(root,digest)
	if err!=nil{return err}
	cmd:=exec.Command(path,"--config-ignore","--version")
	cmd.Dir=root
	cmd.WaitDelay=2*time.Second
	var out,stderr probeOutput
	cmd.Stdout,cmd.Stderr=&out,&stderr
	err=cmd.Run()
	result:=runtimeVersionResult{Started:cmd.Process!=nil,Overflow:out.overflow||stderr.overflow,Stdout:out.Bytes(),Stderr:stderr.Bytes()}
	if err!=nil{
		var exit *exec.ExitError
		if errors.As(err,&exit){result.ExitCode=exit.ExitCode()}else{result.Failed=true}
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

// The custodian owns the pipe reader and dormant operation until Stop.
// Version can wait for bounded output concurrently with a shutdown Stop;
// closing the reader is safe and never substitutes for containment proof.
type runtimeVersionOperation struct{
	processguard.OperationChild
	reader *os.File
}
func(o *runtimeVersionOperation)Stop(ctx context.Context)error{
	err:=o.OperationChild.Stop(ctx)
	_ = o.reader.Close()
	return err
}
func(o *runtimeVersionOperation)Version(ctx context.Context)(string,error){
	type readResult struct{body []byte;err error}
	read:=make(chan readResult,1)
	go func(){b,e:=io.ReadAll(io.LimitReader(o.reader,runtimeProbeResultLimit+1));read<-readResult{b,e}}()
	var received readResult
	select{case received=<-read:case <-ctx.Done():return "",ctx.Err()}
	if received.err!=nil||len(received.body)>runtimeProbeResultLimit{return "",errRuntimeProbe}
	var output runtimeVersionResult
	if err:=json.Unmarshal(received.body,&output);err!=nil{return "",errRuntimeProbe}
	return output.version()
}
func runtimeVersionStarter(digest string)processguard.VersionProbeStarter{
	return func(ctx context.Context)(processguard.VersionProbeChild,error){
		if err:=ctx.Err();err!=nil{return nil,err}
		r,w,err:=os.Pipe()
		if err!=nil{return nil,err}
		defer w.Close()
		child,err:=startRuntimeVersionPlatform(w,digest)
		if child==nil{_ = r.Close();return nil,err}
		return &runtimeVersionOperation{OperationChild:child,reader:r},err
	}
}

func runtimeProbeDigest()(string,error){
	root,err:=runtimeProbeRoot()
	if err!=nil{return "",err}
	digest,err:=readRuntimeProbeDigest(root)
	if err!=nil{return "",err}
	if _,err:=verifyRuntimeProbeBinary(root,digest);err!=nil{return "",err}
	return digest,nil
}

// Keep the original direct diagnostic as a separate baseline.
func runRuntimeVersionProbe()(result error){
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	digest,err:=runtimeProbeDigest()
	if err!=nil{return err}
	ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second)
	defer cancel()
	set:=processguard.NewOperationSet(1)
	defer func(){
		cleanup,done:=context.WithTimeout(context.Background(),10*time.Second)
		defer done()
		result=errors.Join(result,set.SealAndStop(cleanup))
	}()
	var child processguard.VersionProbeChild
	lease,err:=set.Start(ctx,func(start context.Context)(processguard.OperationChild,error){
		var err error
		child,err=runtimeVersionStarter(digest)(start)
		if child==nil{return nil,err}
		return child,err
	})
	if err!=nil{return err}
	version,err:=child.Version(ctx)
	if err!=nil{return err}
	if err:=lease.Stop(ctx);err!=nil{return err}
	if err:=set.SealAndStop(ctx);err!=nil{return err}
	fmt.Println("PASS: contained gallery-dl version probe:",version)
	return nil
}

// Full diagnostic path: library -> actual worker -> private custody
// broker -> fixed runtime -> cleanup -> typed result. Local version
// inspection is allowed offline; it grants no WAN permission.
func runRuntimeBrokerProbe()(result error){
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	if _,err:=runtimeProbeDigest();err!=nil{return err}
	temp,err:=filepath.EvalSymlinks(os.TempDir())
	if err!=nil{return err}
	dir,err:=os.MkdirTemp(temp,managedProbePrefix)
	if err!=nil{return err}
	safe:=false
	defer func(){
		if safe{result=errors.Join(result,os.RemoveAll(dir))}else{fmt.Fprintln(os.Stderr,"retained disposable runtime broker profile:",dir)}
	}()
	ctx,cancel:=context.WithTimeout(context.Background(),40*time.Second)
	defer cancel()
	m,store,generation,err:=crashProbeManager(ctx,dir,"internal-managed-runtime-custody-probe-v1")
	if err!=nil{return err}
	defer store.Close()
	defer func(){
		stop,done:=context.WithTimeout(context.Background(),20*time.Second)
		defer done()
		result=errors.Join(result,m.Shutdown(stop))
	}()
	l,err:=m.Acquire(ctx)
	if err!=nil{return err}
	defer l.Release()
	if err:=checkCrashHealth(ctx,m,l,false);err!=nil{return err}
	p,ok:=l.Peer.(*acquisitionpolicy.CustodySessionPeer)
	if !ok{return errRuntimeProbe}
	check:=func()(string,error){
		e,err:=p.Session().Call(ctx,workerproto.RuntimeStatus,nil)
		if err!=nil{return "",err}
		if e.Error!=nil{return "",errRuntimeProbe}
		v,err:=workerproto.DecodeVersionProbeResult(e.Payload)
		return v.Version,err
	}
	offlineVersion,err:=check()
	if err!=nil{return err}
	if err:=checkCrashHealth(ctx,m,l,false);err!=nil{return err}
	if err:=m.SetOffline(ctx,false);err!=nil{return err}
	if err:=checkCrashHealth(ctx,m,l,true);err!=nil{return err}
	onlineVersion,err:=check()
	if err!=nil||onlineVersion!=offlineVersion{return errors.Join(errRuntimeProbe,err)}
	if err:=checkCrashHealth(ctx,m,l,true);err!=nil{return err}
	if err:=m.SetOffline(ctx,true);err!=nil{return err}
	if err:=checkCrashHealth(ctx,m,l,false);err!=nil{return err}
	if err:=m.Shutdown(ctx);err!=nil{return err}
	if err:=waitCrashReceipt(ctx,dir,*generation);err!=nil{return err}
	safe=true
	fmt.Println("PASS: brokered gallery-dl version probe:",offlineVersion)
	return nil
}
