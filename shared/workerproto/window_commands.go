package workerproto

import(
	"bytes"
	"encoding/json"
)

// WindowAllocateProposal carries policy evidence, not authority. The
// library must compare against independently obtained native evidence
// and current policy before allocating. Large projections need a
// separate bounded artifact transport; this first wire path refuses
// oversized requests rather than truncating them.
type WindowAllocateProposal struct{
	Transfer TransferPlanRequest `json:"transfer"`
	Projection []byte `json:"projection"`
	PolicySHA256 string `json:"policy_sha256"`
}
type WindowCommitRequest struct{
	Allocation StageAllocation `json:"allocation"`
	PolicySHA256 string `json:"policy_sha256"`
}
const MaxWindowProjectionBytes=8<<20
func EncodeWindowAllocateProposal(q WindowAllocateProposal)(json.RawMessage,error){
	if !ValidTransferPlan(q.Transfer)||len(q.Projection)==0||len(q.Projection)>MaxWindowProjectionBytes||!validWindowHash(q.PolicySHA256){return nil,ErrMessage}
	raw,err:=json.Marshal(q)
	if err!=nil||len(raw)>MaxControlBytes-4096{return nil,ErrMessage}
	return raw,nil
}
func DecodeWindowAllocateProposal(raw []byte)(WindowAllocateProposal,error){
	var q WindowAllocateProposal
	if len(raw)>MaxControlBytes-4096||json.Unmarshal(raw,&q)!=nil{return WindowAllocateProposal{},ErrMessage}
	canonical,err:=EncodeWindowAllocateProposal(q)
	if err!=nil||!bytes.Equal(raw,canonical){return WindowAllocateProposal{},ErrMessage}
	return q,nil
}
func EncodeWindowCommitRequest(q WindowCommitRequest)(json.RawMessage,error){
	if !ValidStageAllocation(q.Allocation)||!validWindowHash(q.PolicySHA256){return nil,ErrMessage}
	raw,err:=json.Marshal(q);if err!=nil{return nil,ErrMessage};return raw,nil
}
func DecodeWindowCommitRequest(raw []byte)(WindowCommitRequest,error){
	var q WindowCommitRequest
	if len(raw)>64<<10||json.Unmarshal(raw,&q)!=nil{return WindowCommitRequest{},ErrMessage}
	canonical,err:=EncodeWindowCommitRequest(q);if err!=nil||!bytes.Equal(raw,canonical){return WindowCommitRequest{},ErrMessage}
	return q,nil
}
