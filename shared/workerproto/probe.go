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
	var mu sync.Mutex
	offline,reserved,closed:=true,false,false
	handler:=func(ctx context.Context,op Operation,payload json.RawMessage)(json.RawMessage,*Fault){
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err()!=nil{return nil,&Fault{Code:"canceled"}}
		invalid:=func()(json.RawMessage,*Fault){return nil,&Fault{Code:"invalid"}}
		if op!=SetOffline&&len(payload)!=0{return invalid()}
		switch op{
		case Health:
			body,_:=json.Marshal(struct{
				Probe bool `json:"probe"`
				Offline bool `json:"offline"`
			}{true,offline})
			return body,nil
		case SetOffline:
			p,err:=DecodeOfflinePolicy(payload)
			if err!=nil{return invalid()}
			if (reserved||closed)&&!p.Offline{return nil,&Fault{Code:"closed"}}
			offline=p.Offline
			body,_:=json.Marshal(OfflineAck{Offline:offline,Drained:offline})
			return body,nil
		case ReserveIdle:
			offline,reserved=true,true
			return json.RawMessage("{\"reserved\":true}"),nil
		case Shutdown:
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
