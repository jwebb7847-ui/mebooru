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


// WindowNoTransferProposal reports an exact ordered listing with no
// approved rows. It is not proof of a blacklist decision. Only the
// library's independent native evidence and current policy may settle it.
type WindowNoTransferProposal struct{
	Request MetadataPlanRequest `json:"request"`
	ListedIDs []string `json:"listed_ids"`
	BlockedIDs []string `json:"blocked_ids"`
}
func EncodeWindowNoTransfer(q WindowNoTransferProposal)(json.RawMessage,error){
	if !ValidMetadataPlan(q.Request)||q.Request.Kind!=MetadataStrictResolve||q.ListedIDs==nil||q.BlockedIDs==nil||
		len(q.ListedIDs)!=len(q.BlockedIDs)||len(q.ListedIDs)>int(q.Request.Last-q.Request.First+1){return nil,ErrMessage}
	seen:=map[string]bool{}
	for i,id:=range q.ListedIDs{
		if got,ok:=metadataID(json.RawMessage(id));!ok||got!=id||seen[id]||q.BlockedIDs[i]!=id{return nil,ErrMessage}
		seen[id]=true
	}
	b,err:=json.Marshal(q);if err!=nil||len(b)>128<<10{return nil,ErrMessage};return b,nil
}
func DecodeWindowNoTransfer(b []byte)(WindowNoTransferProposal,error){
	var q WindowNoTransferProposal
	if len(b)>128<<10||json.Unmarshal(b,&q)!=nil{return q,ErrMessage}
	raw,err:=EncodeWindowNoTransfer(q);if err!=nil||!bytes.Equal(raw,b){return WindowNoTransferProposal{},ErrMessage}
	return q,nil
}


// WindowPrepareRequest carries the exact persisted submission ID back
// to the owner. The owner independently reads both durable records.
type WindowPrepareRequest struct{
	Request MetadataPlanRequest `json:"request"`
	IntentID string `json:"intent_id"`
}
func EncodeWindowPrepare(q WindowPrepareRequest)(json.RawMessage,error){
	if !ValidMetadataPlan(q.Request)||q.Request.Kind!=MetadataStrictResolve||!ValidQueueIntentID(q.IntentID){return nil,ErrMessage}
	b,err:=json.Marshal(q);if err!=nil||len(b)>64<<10{return nil,ErrMessage};return b,nil
}
func DecodeWindowPrepare(b []byte)(WindowPrepareRequest,error){
	var q WindowPrepareRequest
	if len(b)>64<<10||json.Unmarshal(b,&q)!=nil{return q,ErrMessage}
	canonical,err:=EncodeWindowPrepare(q);if err!=nil||!bytes.Equal(canonical,b){return WindowPrepareRequest{},ErrMessage}
	return q,nil
}
