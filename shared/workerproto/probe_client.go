package workerproto

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrProbeBrokerRefused=errors.New("operation probe broker refused request")

// ProbeClient is diagnostic-only. On any ambiguous start or stop it
// closes the channel, forcing the custodian to seal all operations.
// The client must not interpret channel loss as successful cleanup.
type ProbeClient struct{session *Session}
func NewProbeClient(s *Session)(*ProbeClient,error){
	if s==nil||s.role!=OperationRequester{return nil,ErrDirection}
	return &ProbeClient{session:s},nil
}
func(c *ProbeClient)Start(ctx context.Context)(ProbeOperationRef,error){
	return c.start(ctx,OperationProbeStart)
}
func(c *ProbeClient)start(ctx context.Context,op Operation)(ProbeOperationRef,error){
	e,err:=c.session.Call(ctx,op,nil)
	if err==nil&&op==OperationTransferProbeStart&&e.Error!=nil&&e.Error.Code=="offline"{return ProbeOperationRef{},ErrOffline}
	if err!=nil||e.Error!=nil{
		c.session.Close()
		return ProbeOperationRef{},errors.Join(ErrProbeBrokerRefused,err)
	}
	ref,err:=DecodeProbeOperationRef(e.Payload)
	if err!=nil{c.session.Close()}
	return ref,err
}
func(c *ProbeClient)Stop(ctx context.Context,ref ProbeOperationRef)error{
	if ref.ID==0{return ErrMessage}
	body,err:=json.Marshal(ref)
	if err!=nil{return err}
	e,err:=c.session.Call(ctx,OperationProbeStop,body)
	if err!=nil||e.Error!=nil{c.session.Close();return errors.Join(ErrProbeBrokerRefused,err)}
	got,err:=DecodeProbeOperationRef(e.Payload)
	if err!=nil||got!=ref{c.session.Close();return ErrMessage}
	return nil
}

// Version asks only for the fixed local diagnostic version command.
// A valid response means the custodian reported completed cleanup;
// cancellation/transport loss never constitutes that proof.
func(c *ProbeClient)Version(ctx context.Context)(VersionProbeResult,error){
	e,err:=c.session.Call(ctx,OperationVersionProbe,nil)
	if err!=nil||e.Error!=nil{
		c.session.Close()
		return VersionProbeResult{},errors.Join(ErrProbeBrokerRefused,err)
	}
	v,err:=DecodeVersionProbeResult(e.Payload)
	if err!=nil{c.session.Close()}
	return v,err
}
