package acquisitionplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

var ErrStrictTransferPlan = errors.New("strict transfer plan refused")

// StrictEvidenceField is deliberately shared with the existing predicate.
// Any new/changed native tag or rating evidence must fail that predicate.
func StrictEvidenceField(key string)bool{
	return key=="tags"||key=="tag_ids"||key=="rating"||strings.HasPrefix(key,"tags_")
}

// StrictTransferPlan retains the exact immutable native evidence for the
// subset approved by the trusted evaluator. Construct ONLY from the
// owner-validated metadata window and policy verdict, never requester
// lists. It is not a launch permission, durable receipt or staging grant.
type StrictTransferPlan struct{state *strictTransferState}
type strictTransferState struct{
	request workerproto.TransferPlanRequest
	identity MetadataIdentity
	manifest []byte
	used atomic.Bool
}

// NewStrictTransferPlan validates native rows again, excludes every
// nonapproved row, and binds the result to the qualified runtime,
// config profile and rule generation. An empty allowlist has no plan.
func NewStrictTransferPlan(q workerproto.MetadataPlanRequest,subcategory string,
	posts []workerproto.MetadataPost,approved []string,policyGeneration uint64,
	identity MetadataIdentity,config MetadataConfig)(*StrictTransferPlan,error){
	if !workerproto.ValidMetadataPlan(q)||q.Kind!=workerproto.MetadataStrictResolve||
		!StrictSource(q.Source,subcategory)||len(posts)>int(q.Last-q.First+1)||
		len(approved)==0||len(approved)>len(posts)||policyGeneration==0||
		!identity.Runtime.valid()||config.Source!=q.Source{return nil,ErrStrictTransferPlan}
	cfg,err:=config.Identity()
	if err!=nil||cfg!=identity.Config{return nil,ErrStrictTransferPlan}
	selected:=make(map[string]bool,len(approved))
	for _,id:=range approved{
		if id==""||selected[id]{return nil,ErrStrictTransferPlan}
		selected[id]=true
	}
	seen:=make(map[string]bool,len(posts))
	records:=make(map[string]map[string]json.RawMessage,len(approved))
	total:=0
	for _,p:=range posts{
		if seen[p.ID]||len(p.Native)>workerproto.MaxMetadataPostBytes||len(p.MediaURL)>workerproto.MaxMetadataURLBytes{
			return nil,ErrStrictTransferPlan
		}
		seen[p.ID]=true
		row,err:=json.Marshal([]any{[]any{3,p.MediaURL,p.Native}})
		total+=len(row)
		if err!=nil||total>workerproto.MaxMetadataOutputBytes{return nil,ErrStrictTransferPlan}
		parsed,err:=workerproto.DecodeMetadataEvidence(row,q,subcategory)
		if err!=nil||len(parsed)!=1||parsed[0].ID!=p.ID{return nil,ErrStrictTransferPlan}
		if !selected[p.ID]{continue}
		var meta map[string]json.RawMessage
		if json.Unmarshal(parsed[0].Native,&meta)!=nil{return nil,ErrStrictTransferPlan}
		media,err:=json.Marshal(p.MediaURL)
		if err!=nil{return nil,ErrStrictTransferPlan}
		record:=map[string]json.RawMessage{"category":meta["category"],"subcategory":meta["subcategory"],StrictMediaURLKey:media}
		for k,v:=range meta{if StrictEvidenceField(k){record[k]=v}}
		records[p.ID]=record
	}
	if len(records)!=len(selected){return nil,ErrStrictTransferPlan}
	manifest,err:=json.Marshal(records)
	if err!=nil||len(manifest)>workerproto.MaxMetadataOutputBytes{return nil,ErrStrictTransferPlan}
	sum:=sha256.Sum256(manifest)
	request:=workerproto.TransferPlanRequest{Metadata:q,Subcategory:subcategory,PolicyGeneration:policyGeneration,ManifestSHA256:hex.EncodeToString(sum[:])}
	if !workerproto.ValidTransferPlan(request){return nil,ErrStrictTransferPlan}
	return &StrictTransferPlan{state:&strictTransferState{request:request,identity:identity,manifest:manifest}},nil
}

func(p *StrictTransferPlan) Request()workerproto.TransferPlanRequest{
	if p==nil||p.state==nil{return workerproto.TransferPlanRequest{}}
	return p.state.request
}

// Consume returns an independent manifest copy once, only for the exact
// matching request and current trusted identity and policy generation.
// The future custodian must also atomically admit the original online
// generation, reserve rooted staging, run predicate probes and prove
// containment cleanup. A failed matching attempt cannot be retried.
func(p *StrictTransferPlan)Consume(q workerproto.TransferPlanRequest,observed MetadataIdentity,policyGeneration uint64)([]byte,error){
	if p==nil||p.state==nil{return nil,ErrStrictTransferPlan}
	s:=p.state
	if q!=s.request||!s.used.CompareAndSwap(false,true){return nil,ErrStrictTransferPlan}
	if observed!=s.identity||policyGeneration!=s.request.PolicyGeneration{return nil,ErrStrictTransferPlan}
	return append([]byte(nil),s.manifest...),nil
}
