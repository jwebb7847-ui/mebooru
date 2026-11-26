package workerproto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Diagnostic only: library receives a count and digest of every byte
// actually consumed by the worker, not raw extractor output or paths.
type CatalogProbeResult struct{
	Bytes uint64 `json:"bytes"`
	SHA256 string `json:"sha256"`
}
func DecodeCatalogProbeResult(body []byte)(CatalogProbeResult,error){
	var v CatalogProbeResult
	if err:=decodeObject(body,&v,"bytes","sha256");err!=nil||v.Bytes==0||v.Bytes>StreamOutputLimit{return CatalogProbeResult{},ErrMessage}
	raw,err:=hex.DecodeString(v.SHA256)
	if err!=nil||len(raw)!=sha256.Size||hex.EncodeToString(raw)!=v.SHA256{return CatalogProbeResult{},ErrMessage}
	return v,nil
}
func readProbeCatalog(ctx context.Context,s *Session)(out CatalogProbeResult,result error){
	c,err:=NewStreamProbeClient(s)
	if err!=nil{return out,err}
	ref,err:=c.Start(ctx)
	if err!=nil{return out,err}
	terminal:=false
	defer func(){
		if terminal{return}
		// A known canceled read can stop only its own operation. Never
		// pass a canceled caller context into the cleanup request.
		stop,done:=context.WithTimeout(context.WithoutCancel(ctx),12*time.Second)
		defer done()
		result=errors.Join(result,c.Stop(stop,ref))
	}()
	hash:=sha256.New()
	q:=StreamCursor{ID:ref.ID}
	for{
		chunk,err:=c.Read(ctx,q)
		if err!=nil{return out,err}
		_,_=hash.Write(chunk.Data)
		q.Offset+=uint64(len(chunk.Data))
		if chunk.EOF{break}
	}
	v,err:=c.Finish(ctx,q)
	// Finish ambiguity closes the diagnostic session; it is no proof.
	terminal=true
	if err!=nil{return out,err}
	if v.ExitCode!=0||len(v.Stderr)!=0||q.Offset==0{return out,ErrMessage}
	return CatalogProbeResult{Bytes:q.Offset,SHA256:hex.EncodeToString(hash.Sum(nil))},nil
}
