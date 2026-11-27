package pipeline

import(
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/kwdict"
	"github.com/monbooru/monloader/internal/mapping"
)

// This is the approved policy interpretation, NOT a final import DTO.
// Native evidence remains independent and unchanged. Optional commentary,
// notes, post file facts, user extras and duplicate semantics still need
// full source/provenance verification and the library's existing services.
// Keep the actual mapped tags and rating (including explicit overrides),
// not a later reconstruction from mutable category/rule state.
type approvedPolicyRow struct{
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
	Taxonomy []kwdict.InterpretedTag `json:"taxonomy"`
}
type approvedPolicySnapshot struct{
	Version int `json:"version"`
	Transfer workerproto.TransferPlanRequest `json:"transfer"`
	Rows []approvedPolicyRow `json:"rows"`
}
const maxPolicyProjectionBytes=64<<20
const maxPolicyProjectionRowBytes=4<<20

func policyRow(m *mapping.Mapper,row ManagedEvidencePost,native workerproto.MetadataPost)(approvedPolicyRow,error){
	if m==nil||row.Item.ID!=native.ID||row.Item.MediaURL!=native.MediaURL||len(native.Native)==0||
		mapping.PostID(row.Item.Meta)!=native.ID||m.ValidateSourceRating(row.Interpreted)!=nil{return approvedPolicyRow{},ErrManagedPreflight}
	pf:=m.Map(row.Interpreted)
	sum:=sha256.Sum256(native.Native)
	// Deep-copy the typed taxonomy by value, including nested alias slices.
	taxonomy, _:=row.Interpreted[kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag)
	taxonomy=slices.Clone(taxonomy)
	for i:=range taxonomy{taxonomy[i].Aliases=slices.Clone(taxonomy[i].Aliases)}
	p:=approvedPolicyRow{PostID:native.ID,NativeSHA256:hex.EncodeToString(sum[:]),MediaURL:native.MediaURL,
		SourceLabel:pf.Source,PostURL:m.PostURL(row.Interpreted),Rating:pf.Rating,
		Tags:slices.Clone(pf.Tags),WithheldTags:slices.Clone(pf.WithheldTags),
		WithheldCategories:slices.Clone(pf.WithheldCategories),UnknownTargets:slices.Clone(pf.UnknownTargets),Taxonomy:taxonomy}
	body,err:=json.Marshal(p)
	if err!=nil||len(body)>maxPolicyProjectionRowBytes{return approvedPolicyRow{},ErrManagedPreflight}
	return p,nil
}
func encodeApprovedPolicy(q workerproto.TransferPlanRequest,rows []approvedPolicyRow)([]byte,string,error){
	if !workerproto.ValidTransferPlan(q)||len(rows)==0||len(rows)>int(q.Metadata.Last-q.Metadata.First+1){return nil,"",ErrManagedPreflight}
	seen:=make(map[string]bool,len(rows))
	var size int
	for _,r:=range rows{
		if r.PostID==""||seen[r.PostID]{return nil,"",ErrManagedPreflight}
		seen[r.PostID]=true
		body,err:=json.Marshal(r)
		if err!=nil||len(body)>maxPolicyProjectionRowBytes{return nil,"",ErrManagedPreflight}
		size+=len(body)
		if size>maxPolicyProjectionBytes{return nil,"",ErrManagedPreflight}
	}
	body,err:=json.Marshal(approvedPolicySnapshot{Version:1,Transfer:q,Rows:rows})
	if err!=nil||len(body)>maxPolicyProjectionBytes{return nil,"",ErrManagedPreflight}
	sum:=sha256.Sum256(body)
	return body,hex.EncodeToString(sum[:]),nil
}

// policyEvidence is a local read-only candidate, not an authorization
// or a transport method. Exact transfer and current owner observation
// required; copied bytes cannot mutate approval. The original online
// epoch is checked but not admitted (no network or launch). A future
// library import capability must independently bind and validate this
// digest and full provenance/metadata under its own durable authority.
func(a *ManagedTransferApproval)policyEvidence(ctx context.Context,q workerproto.TransferPlanRequest)([]byte,string,error){
	if a==nil||a.preflight==nil||a.plan==nil||q!=a.Request()||len(a.projection)==0{return nil,"",ErrManagedPreflight}
	p:=a.preflight
	if !p.current(ctx)||p.window.gate.CurrentGeneration()!=p.window.generation{return nil,"",ErrManagedPreflight}
	sum:=sha256.Sum256(a.projection)
	if hex.EncodeToString(sum[:])!=a.projectionSHA256{return nil,"",ErrManagedPreflight}
	return bytes.Clone(a.projection),a.projectionSHA256,nil
}
