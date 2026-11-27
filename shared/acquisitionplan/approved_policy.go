package acquisitionplan

import(
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// ApprovedPolicyTag retains verified source taxonomy separately from
// the native extractor and mapped tags. It is not independently verified
// merely because it has this shape.
type ApprovedPolicyTag struct{
	Listed string
	Category string
	Name string
	Aliases []string
	LegacyBlacklistName string
}
type ApprovedPolicyRow struct{
	PostID string `json:"post_id"`
	NativeSHA256 string `json:"native_sha256"`
	MediaURL string `json:"media_url"`
	SourceLabel string `json:"source_label"`
	PostURL string `json:"post_url"`
	Rating string `json:"rating"`
	Tags []string `json:"tags"`
	WithheldTags []string `json:"withheld_tags"`
	WithheldCategories []string `json:"withheld_categories"`
	UnknownTargets []string `json:"unknown_targets"`
	Taxonomy []ApprovedPolicyTag `json:"taxonomy"`
}
type ApprovedPolicySnapshot struct{
	Version int `json:"version"`
	Transfer workerproto.TransferPlanRequest `json:"transfer"`
	Rows []ApprovedPolicyRow `json:"rows"`
}
const MaxPolicyProjectionBytes=64<<20
const MaxPolicyProjectionRowBytes=4<<20
const maxPolicyStringBytes=4096
const maxPolicyValues=4*workerproto.MaxMetadataTagCount

func policyDigest(s string)bool{
	raw,err:=hex.DecodeString(s)
	return err==nil&&len(raw)==sha256.Size&&hex.EncodeToString(raw)==s&&s!=strings.Repeat("0",64)
}
func policyText(s string,required bool)bool{
	return utf8.ValidString(s)&&len(s)<=maxPolicyStringBytes&&(!required||strings.TrimSpace(s)!="")
}
func policyStrings(values []string)bool{
	if len(values)>maxPolicyValues{return false}
	for _,v:=range values{if !policyText(v,true){return false}}
	return true
}
func validPolicyRow(r ApprovedPolicyRow)bool{
	id,err:=strconv.ParseUint(r.PostID,10,64)
	if err!=nil||id==0||strconv.FormatUint(id,10)!=r.PostID||!policyDigest(r.NativeSHA256)||
		!policyText(r.MediaURL,true)||!policyText(r.SourceLabel,true)||!policyText(r.PostURL,false)||
		!policyStrings(r.Tags)||!policyStrings(r.WithheldTags)||!policyStrings(r.WithheldCategories)||
		!policyStrings(r.UnknownTargets)||len(r.Taxonomy)>maxPolicyValues{return false}
	switch r.Rating{
	case "","general","sensitive","questionable","explicit":
	default:return false
	}
	ratings:=0
	for _,tag:=range r.Tags{
		if strings.HasPrefix(tag,"rating:"){
			if r.Rating==""||tag!="rating:"+r.Rating{return false}
			ratings++
		}
	}
	if (r.Rating==""&&ratings!=0)||(r.Rating!=""&&ratings!=1){return false}
	for _,t:=range r.Taxonomy{
		if !policyText(t.Listed,true)||!policyText(t.Category,true)||!policyText(t.Name,true)||
			!policyText(t.LegacyBlacklistName,false)||!policyStrings(t.Aliases){return false}
	}
	return true
}

// EncodeApprovedPolicy only serializes already trusted mapped evidence;
// it cannot authorize a post, validate a rule, or grant import.
func EncodeApprovedPolicy(q workerproto.TransferPlanRequest,rows []ApprovedPolicyRow)([]byte,string,error){
	if !workerproto.ValidTransferPlan(q)||len(rows)==0||len(rows)>int(q.Metadata.Last-q.Metadata.First+1){return nil,"",ErrStrictTransferPlan}
	seen:=make(map[string]bool,len(rows))
	var size int
	for _,r:=range rows{
		if !validPolicyRow(r)||seen[r.PostID]{return nil,"",ErrStrictTransferPlan}
		seen[r.PostID]=true
		body,err:=json.Marshal(r)
		if err!=nil||len(body)>MaxPolicyProjectionRowBytes||len(body)>MaxPolicyProjectionBytes-size{return nil,"",ErrStrictTransferPlan}
		size+=len(body)
	}
	body,err:=json.Marshal(ApprovedPolicySnapshot{Version:1,Transfer:q,Rows:rows})
	if err!=nil||len(body)>MaxPolicyProjectionBytes{return nil,"",ErrStrictTransferPlan}
	sum:=sha256.Sum256(body)
	return body,hex.EncodeToString(sum[:]),nil
}

// DecodeApprovedPolicy requires the independently trusted digest and
// exact original transfer manifest from the same approved operation.
// Never take the expected hash from the request body or the worker alone.
// Canonical byte comparison rejects duplicate/unknown JSON keys, and
// every approved row must correspond to exactly one manifest ID/URL.
// It does not independently prove taxonomy truth, policy approval,
// source provenance or final database commit. This is a local artifact
// format (64 MiB cap), not a single control-frame DTO.
func DecodeApprovedPolicy(body []byte,q workerproto.TransferPlanRequest,expectedDigest string,manifest []byte)(ApprovedPolicySnapshot,error){
	var zero ApprovedPolicySnapshot
	if !policyDigest(expectedDigest)||len(body)==0||len(body)>MaxPolicyProjectionBytes||!utf8.Valid(body){return zero,ErrStrictTransferPlan}
	sum:=sha256.Sum256(body)
	if hex.EncodeToString(sum[:])!=expectedDigest{return zero,ErrStrictTransferPlan}
	var snapshot ApprovedPolicySnapshot
	if json.Unmarshal(body,&snapshot)!=nil||snapshot.Version!=1||snapshot.Transfer!=q{return zero,ErrStrictTransferPlan}
	canonical,digest,err:=EncodeApprovedPolicy(q,snapshot.Rows)
	if err!=nil||digest!=expectedDigest||!bytes.Equal(body,canonical){return zero,ErrStrictTransferPlan}
	verifier,err:=NewStrictManifestVerifier(q,manifest)
	if err!=nil||len(verifier.approved)!=len(snapshot.Rows){return zero,ErrStrictTransferPlan}
	for _,r:=range snapshot.Rows{
		record,ok:=verifier.approved[r.PostID]
		if !ok{return zero,ErrStrictTransferPlan}
		var url,source,subcategory string
		if json.Unmarshal(record[StrictMediaURLKey],&url)!=nil||url!=r.MediaURL||
			json.Unmarshal(record["category"],&source)!=nil||source!=q.Metadata.Source||
			json.Unmarshal(record["subcategory"],&subcategory)!=nil||subcategory!=q.Subcategory{return zero,ErrStrictTransferPlan}
	}
	return snapshot,nil
}
