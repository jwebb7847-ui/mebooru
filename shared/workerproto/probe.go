package workerproto

import (
	"context"
	"encoding/json"
	"io"
	"sync"
)

// ServeLifecycleProbe exposes only a zero-work, non-network test worker.
// No acquisition adapters, queue, scheduler, config, paths or media may
// be initialized here. The online bit is a simulated lifecycle state;
// even "online" grants no actual capability. Unknown operations fail closed.
func ServeLifecycleProbe(ctx context.Context,r io.ReadCloser,w io.WriteCloser)error{
	return ServeLifecycleProbeWithBroker(ctx,r,w,nil)
}

// Simulated online starts two fixed leaves and stops one. Offline waits
// for the remaining cleanup proof. No WAN is performed.
func ServeLifecycleProbeWithBroker(ctx context.Context,r io.ReadCloser,w io.WriteCloser,broker *ProbeClient)error{
	return serveLifecycleProbe(ctx,r,w,broker,nil)
}

// ServeWorkerLossProbe is only called by the distinct internal diagnostic
// worker executable entry. A health request after an acknowledged online
// transition deliberately loses its response by exiting that process.
// No extra operation is added to the ordinary protocol vocabulary.
func ServeWorkerLossProbe(ctx context.Context,r io.ReadCloser,w io.WriteCloser,broker *ProbeClient,abort func())error{
	if broker==nil||abort==nil{return ErrMessage}
	return serveLifecycleProbe(ctx,r,w,broker,abort)
}

func serveLifecycleProbe(ctx context.Context,r io.ReadCloser,w io.WriteCloser,broker *ProbeClient,abort func())error{
	var active ProbeOperationRef
	stop:=func(ctx context.Context)error{
		if active.ID==0{return nil}
		if err:=broker.Stop(ctx,active);err!=nil{return err}
		active=ProbeOperationRef{}
		return nil
	}
	var mu sync.Mutex
	offline,reserved,closed:=true,false,false
	handler:=func(ctx context.Context,op Operation,payload json.RawMessage)(json.RawMessage,*Fault){
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err()!=nil{return nil,&Fault{Code:"canceled"}}
		invalid:=func()(json.RawMessage,*Fault){return nil,&Fault{Code:"invalid"}}
		if (op==Health||op==ReserveIdle||op==Shutdown)&&len(payload)!=0{return invalid()}
		switch op{
		case Health:
			if abort!=nil&&active.ID!=0&&!offline{
				abort()
				return nil,&Fault{Code:"diagnostic_abort_returned"}
			}
			body,_:=json.Marshal(struct{
				Probe bool `json:"probe"`
				Offline bool `json:"offline"`
				Operations int `json:"operations"`
			}{true,offline,map[bool]int{true:1}[active.ID!=0]})
			return body,nil
		case SiteCatalog:
			if len(payload)!=0{return invalid()}
			if broker==nil||abort!=nil||closed||reserved{return nil,&Fault{Code:"unsupported"}}
			v,err:=readProbeCatalog(ctx,broker.session)
			if err!=nil{return nil,&Fault{Code:"catalog_probe_failed"}}
			body,_:=json.Marshal(v)
			return body,nil
		case RuntimeStatus:
			// Restricted probe only, not the production runtime status DTO.
			// The custodian independently enables the fixed local operation.
			if len(payload)!=0{return invalid()}
			if broker==nil||abort!=nil||closed||reserved{return nil,&Fault{Code:"unsupported"}}
			v,err:=broker.Version(ctx)
			if err!=nil{return nil,&Fault{Code:"runtime_probe_failed"}}
			body,_:=json.Marshal(v)
			return body,nil
		case SetOffline:
			p,err:=DecodeOfflinePolicy(payload)
			if err!=nil{return invalid()}
			if (reserved||closed)&&!p.Offline{return nil,&Fault{Code:"closed"}}
			if broker!=nil{
				if p.Offline{if err:=stop(ctx);err!=nil{return nil,&Fault{Code:"cleanup_unconfirmed"}}}else if active.ID==0{
					a,err:=broker.Start(ctx);if err!=nil{return nil,&Fault{Code:"start_failed"}}
					active,err=broker.Start(ctx);if err!=nil{return nil,&Fault{Code:"start_failed"}}
					if err:=broker.Stop(ctx,a);err!=nil{return nil,&Fault{Code:"cleanup_unconfirmed"}}
				}
			}
			offline=p.Offline
			body,_:=json.Marshal(OfflineAck{Offline:offline,Drained:offline})
			return body,nil
		case ReserveIdle:
			if err:=stop(ctx);err!=nil{return nil,&Fault{Code:"cleanup_unconfirmed"}}
			offline,reserved=true,true
			return json.RawMessage("{\"reserved\":true}"),nil
		case Shutdown:
			// Leave any active fixed probe for combined custody shutdown.
			// This diagnostic is not a real drained acknowledgement.
			offline,closed=true,true
			return nil,nil
		default:
			return nil,&Fault{Code:"unsupported"}
		}
	}
	s,err:=OpenSession(ctx,r,w,Worker,handler)
	if err!=nil{return err}
	<-s.Done()
	return nil
}
