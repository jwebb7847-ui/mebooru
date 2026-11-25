package acquisitionpolicy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// SessionPeer translates lifecycle calls into the private control protocol.
// A trusted contained-worker launcher must supply stop, which returns nil
// only after confirmed cleanup of the worker AND owned descendants.
// A response is not independent evidence that the worker drained WAN work.
// Do not construct this from arbitrary local servers or plugin endpoints.
type SessionPeer struct{
	session *workerproto.Session
	stop func(context.Context)error
}
var ErrControlRefused=errors.New("managed worker refused lifecycle control")
var ErrBadAcknowledgement=errors.New("managed worker returned invalid lifecycle acknowledgement")

func NewSessionPeer(session *workerproto.Session,stop func(context.Context)error)(*SessionPeer,error){
	if session==nil||stop==nil{return nil,errors.New("missing managed session or cleanup")}
	return &SessionPeer{session:session,stop:stop},nil
}
func(p *SessionPeer)Done()<-chan struct{}{return p.session.Done()}
func(p *SessionPeer)Stop(ctx context.Context)error{return p.stop(ctx)}

func(p *SessionPeer)ApplyOffline(ctx context.Context,offline bool)error{
	payload,err:=json.Marshal(workerproto.OfflinePolicy{Offline:offline})
	if err!=nil{return err}
	e,err:=p.session.Call(ctx,workerproto.SetOffline,payload)
	if err!=nil{return err}
	if e.Error!=nil{return ErrControlRefused}
	ack,err:=workerproto.DecodeOfflineAck(e.Payload)
	if err!=nil||ack.Offline!=offline||(offline&&!ack.Drained){return ErrBadAcknowledgement}
	return nil
}

func(p *SessionPeer)ReserveIdle(ctx context.Context)(bool,error){
	e,err:=p.session.Call(ctx,workerproto.ReserveIdle,nil)
	if err!=nil{return false,err}
	if e.Error!=nil{return false,ErrControlRefused}
	ack,err:=workerproto.DecodeIdleReservation(e.Payload)
	if err!=nil{return false,ErrBadAcknowledgement}
	return ack.Reserved,nil
}
