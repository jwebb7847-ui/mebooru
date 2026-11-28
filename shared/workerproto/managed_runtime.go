package workerproto

import (
	"context"
	"encoding/json"
)

// Exact operation request only; no path, executable, argv, or environment.
type ManagedMetadataStart struct{
	Request MetadataPlanRequest `json:"request"`
}
type ManagedTransferStart struct{
	Allocation StageAllocation `json:"allocation"`
	Stage string `json:"stage"`
}
func DecodeManagedMetadataStart(body []byte)(MetadataPlanRequest,error){
	var v struct{Request json.RawMessage `json:"request"`}
	if len(body)>64<<10||decodeObject(body,&v,"request")!=nil{return MetadataPlanRequest{},ErrMessage}
	return DecodeMetadataPlanRequest(v.Request)
}
func DecodeManagedTransferStart(body []byte,expected StageAllocateRequest)(ManagedTransferStart,error){
	var v struct{Allocation json.RawMessage `json:"allocation"`;Stage string `json:"stage"`}
	if len(body)>64<<10||decodeObject(body,&v,"allocation","stage")!=nil{return ManagedTransferStart{},ErrMessage}
	switch v.Stage{case "version","negative","positive","media":default:return ManagedTransferStart{},ErrMessage}
	a,err:=DecodeStageAllocation(v.Allocation,expected)
	if err!=nil{return ManagedTransferStart{},err}
	return ManagedTransferStart{Allocation:a,Stage:v.Stage},nil
}

// StartManagedMetadata and StartManagedTransfer require the separately
// authenticated custody session. A start refusal/lost response closes
// the channel because a remote handle may have been created.
func(c *StreamProbeClient) StartManagedMetadata(ctx context.Context,q MetadataPlanRequest)(ProbeOperationRef,error){
	if !ValidMetadataPlan(q){return ProbeOperationRef{},ErrMessage}
	body,err:=json.Marshal(ManagedMetadataStart{Request:q});if err!=nil{return ProbeOperationRef{},err}
	return c.startManaged(ctx,OperationMetadataStart,body)
}
func(c *StreamProbeClient) StartManagedTransfer(ctx context.Context,a StageAllocation,stage string)(ProbeOperationRef,error){
	if !ValidStageAllocation(a){return ProbeOperationRef{},ErrMessage}
	body,err:=json.Marshal(ManagedTransferStart{Allocation:a,Stage:stage});if err!=nil{return ProbeOperationRef{},err}
	if _,err:=DecodeManagedTransferStart(body,a.Request);err!=nil{return ProbeOperationRef{},err}
	return c.startManaged(ctx,OperationStrictTransferStart,body)
}
func(c *StreamProbeClient) startManaged(ctx context.Context,op Operation,body json.RawMessage)(ProbeOperationRef,error){
	e,err:=c.session.Call(ctx,op,body)
	if err!=nil||e.Error!=nil{c.session.Close();if err!=nil{return ProbeOperationRef{},err};return ProbeOperationRef{},ErrProbeBrokerRefused}
	v,err:=DecodeProbeOperationRef(e.Payload)
	if err!=nil{c.session.Close()}
	return v,err
}
