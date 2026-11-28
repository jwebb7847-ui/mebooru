package workerproto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

type WindowItemOutcome struct{
	PostID string `json:"post_id"`
	State string `json:"state"`
	Disposition string `json:"disposition,omitempty"`
	ImageID int64 `json:"image_id,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	ReceiptSHA256 string `json:"receipt_sha256,omitempty"`
}
type WindowOutcome struct{
	Allocation StageAllocation `json:"allocation"`
	PolicySHA256 string `json:"policy_sha256"`
	PlanSHA256 string `json:"plan_sha256"`
	Items []WindowItemOutcome `json:"items"`
}
func validWindowHash(s string)bool{
	raw,err:=hex.DecodeString(s)
	return err==nil&&len(raw)==32&&hex.EncodeToString(raw)==s
}
func ValidWindowOutcome(v WindowOutcome,grant StageAllocation,policy string)bool{
	if !ValidStageAllocation(grant)||v.Allocation!=grant||v.PolicySHA256!=policy||!validWindowHash(policy)||!validWindowHash(v.PlanSHA256)||len(v.Items)==0||len(v.Items)>int(grant.Request.Transfer.Metadata.Last-grant.Request.Transfer.Metadata.First+1){return false}
	seen:=map[string]bool{}
	transferred:=0
	for _,i:=range v.Items{
		id,err:=strconv.ParseUint(i.PostID,10,64)
		if err!=nil||id==0||strconv.FormatUint(id,10)!=i.PostID||seen[i.PostID]{return false}
		seen[i.PostID]=true
		switch i.State{
		case "committed":
			if i.Disposition!="new"&&i.Disposition!="held"&&i.Disposition!="restored"{return false}
			if i.ImageID<=0||!validWindowHash(i.SHA256)||!validWindowHash(i.ReceiptSHA256){return false}
			transferred++
		case "unresolved","not_transferred":
			if i.Disposition!=""||i.ImageID!=0||i.SHA256!=""||i.ReceiptSHA256!=""{return false}
			if i.State=="unresolved"{transferred++}
		default:return false
		}
	}
	return transferred<=int(grant.MaxFiles)
}
func EncodeWindowOutcome(v WindowOutcome,grant StageAllocation,policy string)(json.RawMessage,error){
	if !ValidWindowOutcome(v,grant,policy){return nil,ErrMessage}
	raw,err:=json.Marshal(v)
	if err!=nil||len(raw)>1<<20{return nil,ErrMessage}
	return raw,nil
}
func DecodeWindowOutcome(raw []byte,grant StageAllocation,policy string)(WindowOutcome,error){
	var zero WindowOutcome
	var v WindowOutcome
	if len(raw)>1<<20||json.Unmarshal(raw,&v)!=nil{return zero,ErrMessage}
	body,err:=EncodeWindowOutcome(v,grant,policy)
	if err!=nil||!bytes.Equal(body,raw){return zero,ErrMessage}
	return v,nil
}
