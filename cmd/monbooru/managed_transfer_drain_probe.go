package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

const (
	transferSeenMarker = "transfer-seen"
	transferPendingMarker = "transfer-cleanup-pending"
	transferReleaseMarker = "transfer-allow-cleanup"
	transferCompleteMarker = "transfer-cleanup-complete"
)

func markTransfer(dir,name string)error{
	return os.WriteFile(filepath.Join(dir,name),nil,0600)
}

// One-shot, fixture-only transfer capability. The custodian creates
// its own loopback endpoint; neither requester nor library RPC can
// choose a destination. Check the library-owned persisted grant before
// each dormant start. This supplements the worker admission gate, not
// a same-user OS sandbox or an independent production policy channel.
func brokerTransferStarter(digest,dir string)processguard.ProbeStarter{
	used:=false // serialized by the broker's start lock
	return func(ctx context.Context)(processguard.OperationChild,error){
		if used{return nil,errRuntimeProbe}
		store,err:=acquisitionpolicy.Open(dir)
		if err!=nil{return nil,err}
		offline,err:=store.Load()
		_ = store.Close()
		if err!=nil||offline{return nil,workerproto.ErrOffline}
		if err:=ctx.Err();err!=nil{return nil,err}
		listener,err:=net.Listen("tcp4","127.0.0.1:0")
		if err!=nil{return nil,err}
		witness:=&transferWitness{seen:make(chan struct{}),disconnected:make(chan struct{}),
			onSeen:func()error{return markTransfer(dir,transferSeenMarker)}}
		server:=&http.Server{Handler:http.HandlerFunc(witness.serve),ReadHeaderTimeout:5*time.Second}
		go server.Serve(listener)
		port:=strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
		child,err:=startRuntimeTransferPlatform(digest,dir,port)
		if child==nil{_ = server.Close();_ = listener.Close();return nil,err}
		used=true
		return &brokerTransferOperation{OperationChild:child,dir:dir,witness:witness,server:server,listener:listener},err
	}
}

type brokerTransferOperation struct{
	processguard.OperationChild
	dir string
	witness *transferWitness
	server *http.Server
	listener net.Listener
	checked bool // serialized by the operation lease
}

// The exact operation lease serializes Stop. A bounded test barrier
// makes it possible to observe the worker's admission state before
// cleanup; it cannot substitute for actual process and HTTP proof.
func(o *brokerTransferOperation)Stop(ctx context.Context)error{
	var barrierErr error
	if !o.checked{
		o.checked=true
		select{
		case <-o.witness.seen:
			select{
			case <-o.witness.disconnected:barrierErr=errors.New("transfer disconnected before offline cancellation")
			default:
				barrierErr=markTransfer(o.dir,transferPendingMarker)
				if barrierErr==nil{
					barrier,done:=context.WithTimeout(ctx,4*time.Second)
					barrierErr=waitCrashMarker(barrier,o.dir,transferReleaseMarker)
					done()
				}
			}
		default:
		}
	}
	if err:=o.OperationChild.Stop(ctx);err!=nil{return errors.Join(barrierErr,err)}
	select{
	case <-o.witness.seen:
		select{
		case <-o.witness.disconnected:
		case <-ctx.Done():return errors.Join(barrierErr,ctx.Err())
		}
		if err:=markTransfer(o.dir,transferCompleteMarker);err!=nil{return errors.Join(barrierErr,err)}
	default:
	}
	// Only now may the test server be closed. Closing it sooner would
	// manufacture the disconnect witness we are trying to establish.
	_ = o.server.Close()
	_ = o.listener.Close()
	return barrierErr
}

func runRuntimeDrainProbe()(result error){
	if _,err:=processguard.ProbeSibling("monbooru","monloader");err!=nil{return err}
	if _,err:=runtimeProbeDigest();err!=nil{return err}
	temp,err:=filepath.EvalSymlinks(os.TempDir())
	if err!=nil{return err}
	dir,err:=os.MkdirTemp(temp,managedProbePrefix)
	if err!=nil{return err}
	safe:=false
	defer func(){
		if safe{result=errors.Join(result,os.RemoveAll(dir))}else{fmt.Fprintln(os.Stderr,"retained disposable drain profile:",dir)}
	}()
	ctx,cancel:=context.WithTimeout(context.Background(),40*time.Second)
	defer cancel()
	m,store,generation,err:=crashProbeManager(ctx,dir,"internal-managed-runtime-custody-probe-v1")
	if err!=nil{return err}
	defer store.Close()
	defer func(){
		_ = markTransfer(dir,transferReleaseMarker)
		stop,done:=context.WithTimeout(context.Background(),20*time.Second)
		defer done()
		result=errors.Join(result,m.Shutdown(stop))
	}()
	lease,err:=m.Acquire(ctx)
	if err!=nil{return err}
	defer lease.Release()
	p,ok:=lease.Peer.(*acquisitionpolicy.CustodySessionPeer)
	if !ok{return errRuntimeProbe}
	refuse:=func()error{
		call,done:=context.WithTimeout(ctx,time.Second)
		defer done()
		e,err:=p.Session().Call(call,workerproto.TransferProbeStart,nil)
		if err!=nil{return err}
		if e.Error==nil||e.Error.Code!="offline"{return errors.New("transfer admitted while offline")}
		return nil
	}
	if err:=checkCrashHealth(ctx,m,lease,false);err!=nil{return err}
	if err:=refuse();err!=nil{return err}
	if _,err:=os.Lstat(filepath.Join(dir,transferSeenMarker));!errors.Is(err,os.ErrNotExist){return errors.New("offline transfer reached HTTP fixture")}
	if err:=m.SetOffline(ctx,false);err!=nil{return err}
	offline,err:=store.Load()
	if err!=nil||offline{return errors.New("online grant not persisted")}
	e,err:=p.Session().Call(ctx,workerproto.TransferProbeStart,nil)
	if err!=nil{return err}
	if e.Error!=nil{return fmt.Errorf("transfer start refused: %s",e.Error.Code)}
	if _,err:=workerproto.DecodeProbeOperationRef(e.Payload);err!=nil{return err}
	start,done:=context.WithTimeout(ctx,15*time.Second)
	err=waitCrashMarker(start,dir,transferSeenMarker)
	done()
	if err!=nil{return err}
	drained:=make(chan error,1)
	go func(){drained<-m.SetOffline(ctx,true)}()
	// Always release the barrier and join this controller goroutine
	// before shutdown can access the same manager.
	joined:=false
	defer func(){
		_ = markTransfer(dir,transferReleaseMarker)
		if !joined{result=errors.Join(result,<-drained)}
	}()
	wait,done:=context.WithTimeout(ctx,5*time.Second)
	err=waitCrashMarker(wait,dir,transferPendingMarker)
	done()
	if err!=nil{return err}
	if m.EffectiveOnline(){return errors.New("library admission remained open during drain")}
	if err:=refuse();err!=nil{return err}
	select{
	case err:=<-drained:joined=true;return errors.Join(errors.New("offline acknowledged before transfer cleanup"),err)
	default:
	}
	if _,err:=os.Lstat(filepath.Join(dir,transferCompleteMarker));!errors.Is(err,os.ErrNotExist){return errors.New("cleanup completed before release")}
	if err:=markTransfer(dir,transferReleaseMarker);err!=nil{return err}
	err=<-drained
	joined=true
	if err!=nil{return err}
	if err:=waitCrashMarker(ctx,dir,transferCompleteMarker);err!=nil{return err}
	if err:=checkCrashHealth(ctx,m,lease,false);err!=nil{return err}
	if err:=refuse();err!=nil{return err}
	offline,err=store.Load()
	if err!=nil||!offline{return errors.New("offline state not persisted")}
	// Local runtime inspection remains available while offline.
	e,err=p.Session().Call(ctx,workerproto.RuntimeStatus,nil)
	if err!=nil{return err}
	if e.Error!=nil{return errRuntimeProbe}
	if _,err:=workerproto.DecodeVersionProbeResult(e.Payload);err!=nil{return err}
	if err:=m.Shutdown(ctx);err!=nil{return err}
	if err:=waitCrashReceipt(ctx,dir,*generation);err!=nil{return err}
	safe=true
	fmt.Println("PASS: brokered gallery-dl transfer offline drain")
	return nil
}
