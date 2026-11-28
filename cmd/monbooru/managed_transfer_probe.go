package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
)

// Restricted QA only. The server address is minted by the controller,
// never a browser, worker request or real gallery. No redirect, external
// resource, metadata bypass in production, or user-supplied command.
const transferProbeBytes=1<<20
func runRuntimeTransferLeaf(digest,dir,port string)error{
	if err:=validateManagedProbeDir(dir);err!=nil{return err}
	n,err:=strconv.Atoi(port)
	if err!=nil||n<=0||n>65535||strconv.Itoa(n)!=port{return errRuntimeProbe}
	root,err:=runtimeProbeRoot()
	if err!=nil{return err}
	path,err:=verifyRuntimeProbeBinary(root,digest)
	if err!=nil{return err}
	cmd:=exec.Command(path,"--config-ignore","-D",dir,
		"-o","extractor.directlink.enabled=true",
		"-o","extractor.retries=0","-o","extractor.timeout=30",
		"-o","downloader.http.retries=0","-o","downloader.http.timeout=30",
		"http://127.0.0.1:"+port+"/mebooru-synthetic.jpg")
	cmd.Dir=dir
	// Ambient proxy settings must not turn the controlled loopback
	// fixture into a real network destination.
	for _,item:=range os.Environ(){
		key,_,_:=strings.Cut(item,"=")
		if strings.HasSuffix(strings.ToLower(key),"_proxy"){continue}
		cmd.Env=append(cmd.Env,item)
	}
	cmd.Env=append(cmd.Env,"NO_PROXY=*")
	var out,stderr probeOutput
	cmd.Stdout,cmd.Stderr=&out,&stderr
	err=cmd.Run()
	if err!=nil{return errors.New("synthetic gallery-dl transfer did not complete")}
	return nil
}

// Send only a small prefix of a declared response, never the remainder.
// The handler stays blocked until the client disconnects. We never close
// the server to manufacture a successful cancellation witness.
type transferWitness struct{
	seen,disconnected chan struct{}
	once sync.Once
	onSeen func()error
}
func(w *transferWitness)serve(rw http.ResponseWriter,r *http.Request){
	if r.URL.Path!="/mebooru-synthetic.jpg"||r.Method!=http.MethodGet{
		http.NotFound(rw,r);return
	}
	w.once.Do(func(){
		rw.Header().Set("Content-Type","image/jpeg")
		rw.Header().Set("Content-Length",strconv.Itoa(transferProbeBytes))
		rw.WriteHeader(http.StatusOK)
		if _,err:=rw.Write(make([]byte,4096));err!=nil{return}
		if err:=http.NewResponseController(rw).Flush();err!=nil{return}
		close(w.seen)
		// The controller marker is written only after the response was
		// flushed and the in-custodian witness became observable.
		if w.onSeen!=nil{_ = w.onSeen()}
		<-r.Context().Done()
		close(w.disconnected)
	})
}

func runRuntimeTransferCancelProbe()(result error){
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	digest,err:=runtimeProbeDigest()
	if err!=nil{return err}
	temp,err:=filepath.EvalSymlinks(os.TempDir())
	if err!=nil{return err}
	dir,err:=os.MkdirTemp(temp,managedProbePrefix)
	if err!=nil{return err}
	safe:=false
	defer func(){if safe{result=errors.Join(result,os.RemoveAll(dir))}else{fmt.Fprintln(os.Stderr,"retained disposable transfer profile:",dir)}}()
	listener,err:=net.Listen("tcp4","127.0.0.1:0")
	if err!=nil{return err}
	witness:=&transferWitness{seen:make(chan struct{}),disconnected:make(chan struct{})}
	server:=&http.Server{Handler:http.HandlerFunc(witness.serve),ReadHeaderTimeout:5*time.Second}
	defer server.Close()
	go server.Serve(listener)
	port:=strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	ctx,cancel:=context.WithTimeout(context.Background(),35*time.Second)
	defer cancel()
	set:=processguard.NewOperationSet(2)
	defer func(){
		cleanup,done:=context.WithTimeout(context.Background(),10*time.Second)
		defer done()
		result=errors.Join(result,set.SealAndStop(cleanup))
	}()
	var siblingDone <-chan struct{}
	sibling,err:=set.Start(ctx,func(start context.Context)(processguard.OperationChild,error){
		child,err:=startOperationProbe(start)
		if ready,ok:=child.(*readyOperationProbe);ok{
			if live,ok:=ready.OperationChild.(interface{Done()<-chan struct{}});ok{siblingDone=live.Done()}
		}
		if err==nil&&siblingDone==nil{err=errRuntimeProbe}
		return child,err
	})
	if err!=nil{return err}
	lease,err:=set.Start(ctx,func(context.Context)(processguard.OperationChild,error){
		return startRuntimeTransferPlatform(digest,dir,port)
	})
	if err!=nil{return err}
	start,done:=context.WithTimeout(ctx,18*time.Second)
	defer done()
	select{case <-witness.seen:case <-start.Done():return errors.New("synthetic runtime request not observed")}
	select{case <-witness.disconnected:return errors.New("transfer disconnected before cancellation");default:}
	// Force cleanup through the existing platform containment, not an
	// exit signal to a guessed PID, closing the HTTP server, or merely
	// canceling the request context.
	cleanup,stop:=context.WithTimeout(ctx,10*time.Second)
	err=lease.Stop(cleanup)
	stop()
	if err!=nil{return fmt.Errorf("actual transfer containment: %w",err)}
	select{case <-witness.disconnected:case <-ctx.Done():return errors.New("runtime connection persisted after cleanup")}
	// The sibling must still occupy its original lease; no group-wide
	// cancellation of the primary worker or sibling operation.
	select{case <-siblingDone:return errors.New("transfer cancellation terminated independent sibling");default:}
	if err:=sibling.Stop(ctx);err!=nil{return err}
	if err:=set.SealAndStop(ctx);err!=nil{return err}
	safe=true
	fmt.Println("PASS: contained gallery-dl active transfer cancellation")
	return nil
}
