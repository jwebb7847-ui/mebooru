package workerproto

import (
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Initial strict transfer allocation contract. OperationID is a stable
// logical request key for reconciliation, NOT a frame ID or permission.
// The library must independently authorize the exact job, gallery
// identity, original manifest, policy and lifecycle before allocation.
// No caller-provided workspace, final path, quota or approved IDs.
type StageAllocateRequest struct {
	OperationID string `json:"operation_id"`
	Transfer TransferPlanRequest `json:"transfer"`
}

// StageAllocation is a library-issued bounded capability description.
// It is not proof of allocation, writer cleanup, or final commit.
// Resolve its workspace through trusted local allocation state, never
// by interpreting requester path strings. Initial candidate is strict
// transfer only; other acquisition/replace paths remain unimplemented.
type StageAllocation struct {
	Request StageAllocateRequest `json:"request"`
	TransferID string `json:"transfer_id"`
	MaxFiles uint32 `json:"max_files"`
	MaxBytes uint64 `json:"max_bytes"`
}

const MaxStageFileBytes uint64 = 512 << 20
const MaxStageTotalBytes uint64 = 1 << 30

// StageAbandonRequest identifies an allocation; it cannot prove that
// writers stopped or authorize removal of committed/uncertain media.
// The library must check matching scope and independent custody proof.
type StageAbandonRequest struct {
	OperationID string `json:"operation_id"`
	TransferID string `json:"transfer_id"`
}

func validStageID(s string)bool{
	b,err:=hex.DecodeString(s)
	return err==nil&&len(b)==16&&hex.EncodeToString(b)==s&&s!=strings.Repeat("0",32)
}
func ValidStageAllocate(q StageAllocateRequest)bool{
	return validStageID(q.OperationID)&&ValidTransferPlan(q.Transfer)
}
func ValidStageAllocation(a StageAllocation)bool{
	return ValidStageAllocate(a.Request)&&validStageID(a.TransferID)&&a.TransferID!=a.Request.OperationID&&
		a.MaxFiles>0&&a.MaxFiles<=a.Request.Transfer.Metadata.Last-a.Request.Transfer.Metadata.First+1&&
		a.MaxBytes>0&&a.MaxBytes<=MaxStageTotalBytes
}
func ValidStageAbandon(q StageAbandonRequest)bool{
	return validStageID(q.OperationID)&&validStageID(q.TransferID)&&q.OperationID!=q.TransferID
}

func DecodeStageAllocateRequest(body []byte)(StageAllocateRequest,error){
	var wire struct{
		OperationID string `json:"operation_id"`
		Transfer json.RawMessage `json:"transfer"`
	}
	if len(body)>64<<10||decodeObject(body,&wire,"operation_id","transfer")!=nil{return StageAllocateRequest{},ErrMessage}
	transfer,err:=DecodeTransferPlanRequest(wire.Transfer)
	q:=StageAllocateRequest{OperationID:wire.OperationID,Transfer:transfer}
	if err!=nil||!ValidStageAllocate(q){return StageAllocateRequest{},ErrMessage}
	return q,nil
}
func EncodeStageAllocateRequest(q StageAllocateRequest)(json.RawMessage,error){
	if !ValidStageAllocate(q){return nil,ErrMessage}
	body,err:=json.Marshal(q)
	if err!=nil{return nil,ErrMessage}
	if _,err:=DecodeStageAllocateRequest(body);err!=nil{return nil,err}
	return body,nil
}
func DecodeStageAllocation(body []byte,expected StageAllocateRequest)(StageAllocation,error){
	var wire struct{
		Request json.RawMessage `json:"request"`
		TransferID string `json:"transfer_id"`
		MaxFiles uint32 `json:"max_files"`
		MaxBytes uint64 `json:"max_bytes"`
	}
	if len(body)>64<<10||!ValidStageAllocate(expected)||
		decodeObject(body,&wire,"request","transfer_id","max_files","max_bytes")!=nil{return StageAllocation{},ErrMessage}
	q,err:=DecodeStageAllocateRequest(wire.Request)
	a:=StageAllocation{Request:q,TransferID:wire.TransferID,MaxFiles:wire.MaxFiles,MaxBytes:wire.MaxBytes}
	if err!=nil||q!=expected||!ValidStageAllocation(a){return StageAllocation{},ErrMessage}
	return a,nil
}
func EncodeStageAllocation(a StageAllocation,expected StageAllocateRequest)(json.RawMessage,error){
	if a.Request!=expected||!ValidStageAllocation(a){return nil,ErrMessage}
	body,err:=json.Marshal(a)
	if err!=nil{return nil,ErrMessage}
	if _,err:=DecodeStageAllocation(body,expected);err!=nil{return nil,err}
	return body,nil
}
func DecodeStageAbandonRequest(body []byte)(StageAbandonRequest,error){
	var q StageAbandonRequest
	if len(body)>1024||decodeObject(body,&q,"operation_id","transfer_id")!=nil||!ValidStageAbandon(q){return StageAbandonRequest{},ErrMessage}
	return q,nil
}
func EncodeStageAbandonRequest(q StageAbandonRequest)(json.RawMessage,error){
	if !ValidStageAbandon(q){return nil,ErrMessage}
	body,err:=json.Marshal(q)
	if err!=nil{return nil,ErrMessage}
	return body,nil
}
