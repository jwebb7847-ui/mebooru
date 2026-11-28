package workerproto

import (
	"context"
	"encoding/json"
)

const (
	StreamChunkLimit = 32 << 10
	StreamOutputLimit = 64 << 20
	StreamStderrLimit = 4 << 10
)

// Diagnostic output only, never image bytes. Offset is the exact count
// of bytes consumed by the requester. No replay or implicit retry.
type StreamCursor struct {
	ID uint64 `json:"id"`
	Offset uint64 `json:"offset"`
}
type StreamChunk struct {
	StreamCursor
	Data []byte `json:"data"`
	EOF bool `json:"eof"`
}
type StreamTerminal struct {
	StreamCursor
	ExitCode int `json:"exit_code"`
	Stderr []byte `json:"stderr"`
}
func DecodeStreamCursor(body []byte)(StreamCursor,error){
	var q struct{ID uint64 `json:"id"`;Offset *uint64 `json:"offset"`}
	if err:=decodeObject(body,&q,"id","offset");err!=nil||q.ID==0||q.Offset==nil||*q.Offset>StreamOutputLimit{return StreamCursor{},ErrMessage}
	return StreamCursor{ID:q.ID,Offset:*q.Offset},nil
}
func DecodeStreamChunk(body []byte)(StreamChunk,error){
	var q struct{ID uint64 `json:"id"`;Offset *uint64 `json:"offset"`;Data []byte `json:"data"`;EOF *bool `json:"eof"`}
	if err:=decodeObject(body,&q,"id","offset","data","eof");err!=nil||q.ID==0||q.Offset==nil||q.Data==nil||q.EOF==nil{return StreamChunk{},ErrMessage}
	if len(q.Data)>StreamChunkLimit||*q.Offset>StreamOutputLimit||uint64(len(q.Data))>StreamOutputLimit-*q.Offset||(len(q.Data)==0&&!*q.EOF){return StreamChunk{},ErrMessage}
	return StreamChunk{StreamCursor:StreamCursor{ID:q.ID,Offset:*q.Offset},Data:q.Data,EOF:*q.EOF},nil
}
func DecodeStreamTerminal(body []byte)(StreamTerminal,error){
	var q struct{ID uint64 `json:"id"`;Offset *uint64 `json:"offset"`;ExitCode *int `json:"exit_code"`;Stderr []byte `json:"stderr"`}
	if err:=decodeObject(body,&q,"id","offset","exit_code","stderr");err!=nil||q.ID==0||q.Offset==nil||q.ExitCode==nil||q.Stderr==nil{return StreamTerminal{},ErrMessage}
	if *q.Offset>StreamOutputLimit||*q.ExitCode < -1||int64(*q.ExitCode)>2147483647||len(q.Stderr)>StreamStderrLimit{return StreamTerminal{},ErrMessage}
	return StreamTerminal{StreamCursor:StreamCursor{ID:q.ID,Offset:*q.Offset},ExitCode:*q.ExitCode,Stderr:q.Stderr},nil
}

// StreamProbeClient requests fixed diagnostic output, not a command.
// Read/Finish are not retried: on any uncertain read, discard the partial
// result and Stop the exact operation using a fresh cleanup context.
// Only successful Finish or Stop acknowledges containment cleanup.
type StreamProbeClient struct{session *Session}
func NewStreamProbeClient(s *Session)(*StreamProbeClient,error){
	if s==nil||s.role!=OperationRequester{return nil,ErrDirection}
	return &StreamProbeClient{session:s},nil
}
func(c *StreamProbeClient)Start(ctx context.Context)(ProbeOperationRef,error){
	e,err:=c.session.Call(ctx,OperationStreamStart,nil)
	if err!=nil{c.session.Close();return ProbeOperationRef{},err}
	if e.Error!=nil{return ProbeOperationRef{},ErrProbeBrokerRefused}
	v,err:=DecodeProbeOperationRef(e.Payload)
	if err!=nil{c.session.Close()}
	return v,err
}
func(c *StreamProbeClient)Read(ctx context.Context,q StreamCursor)(StreamChunk,error){
	body,_:=json.Marshal(q)
	e,err:=c.session.Call(ctx,OperationStreamRead,body)
	if err!=nil{return StreamChunk{},err}
	if e.Error!=nil{return StreamChunk{},ErrProbeBrokerRefused}
	v,err:=DecodeStreamChunk(e.Payload)
	if err!=nil||v.StreamCursor!=q{return StreamChunk{},ErrMessage}
	return v,nil
}
func(c *StreamProbeClient)Finish(ctx context.Context,q StreamCursor)(StreamTerminal,error){
	body,_:=json.Marshal(q)
	e,err:=c.session.Call(ctx,OperationStreamFinish,body)
	if err!=nil||e.Error!=nil{
		// Finish may have consumed the operation handle. Never infer proof
		// from its absence. Ambiguity closes the entire diagnostic session.
		c.session.Close()
		if err!=nil{return StreamTerminal{},err}
		return StreamTerminal{},ErrProbeBrokerRefused
	}
	v,err:=DecodeStreamTerminal(e.Payload)
	if err!=nil||v.StreamCursor!=q{c.session.Close();return StreamTerminal{},ErrMessage}
	return v,nil
}
func(c *StreamProbeClient)Stop(ctx context.Context,ref ProbeOperationRef)error{
	body,_:=json.Marshal(ref)
	e,err:=c.session.Call(ctx,OperationStreamStop,body)
	if err!=nil||e.Error!=nil{
		c.session.Close()
		if err!=nil{return err}
		return ErrProbeBrokerRefused
	}
	v,err:=DecodeProbeOperationRef(e.Payload)
	if err!=nil||v!=ref{c.session.Close();return ErrMessage}
	return nil
}
