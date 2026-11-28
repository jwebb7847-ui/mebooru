package workerproto

import (
	"encoding/hex"
	"encoding/json"
	"strings"
)

// TransferPlanRequest identifies an independently authorized immutable
// manifest. No post list, predicate, path, executable, config or argv
// is accepted from the requester. A matching request is not a grant.
type TransferPlanRequest struct {
	Metadata MetadataPlanRequest `json:"metadata"`
	Subcategory string `json:"subcategory"`
	PolicyGeneration uint64 `json:"policy_generation"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

func ValidTransferPlan(q TransferPlanRequest) bool {
	digest,err:=hex.DecodeString(q.ManifestSHA256)
	return ValidMetadataPlan(q.Metadata)&&q.Metadata.Kind==MetadataStrictResolve&&
		QualifiedMetadataSource(q.Metadata.Source,q.Subcategory)&&q.PolicyGeneration!=0&&
		err==nil&&len(digest)==32&&hex.EncodeToString(digest)==q.ManifestSHA256&&
		q.ManifestSHA256!=strings.Repeat("0",64)
}

func DecodeTransferPlanRequest(body []byte)(TransferPlanRequest,error){
	var wire struct{
		Metadata json.RawMessage `json:"metadata"`
		Subcategory string `json:"subcategory"`
		PolicyGeneration uint64 `json:"policy_generation"`
		ManifestSHA256 string `json:"manifest_sha256"`
	}
	if len(body)>32<<10||decodeObject(body,&wire,"metadata","subcategory","policy_generation","manifest_sha256")!=nil{
		return TransferPlanRequest{},ErrMessage
	}
	metadata,err:=DecodeMetadataPlanRequest(wire.Metadata)
	if err!=nil{return TransferPlanRequest{},ErrMessage}
	q:=TransferPlanRequest{Metadata:metadata,Subcategory:wire.Subcategory,PolicyGeneration:wire.PolicyGeneration,ManifestSHA256:wire.ManifestSHA256}
	if !ValidTransferPlan(q){return TransferPlanRequest{},ErrMessage}
	return q,nil
}

func EncodeTransferPlanRequest(q TransferPlanRequest)(json.RawMessage,error){
	if !ValidTransferPlan(q){return nil,ErrMessage}
	body,err:=json.Marshal(q)
	if err!=nil{return nil,ErrMessage}
	if _,err:=DecodeTransferPlanRequest(body);err!=nil{return nil,err}
	return body,nil
}
