package pipeline

import(
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/monbooru/monloader/internal/kwdict"
	"github.com/monbooru/monloader/internal/mapping"
)

// This is the approved policy interpretation, NOT a final import DTO.
// Native evidence remains independent and unchanged. Preserve mapped
// source claims too; job extras, duplicate semantics and actual library
// validation/storage are still separate.
// Keep the actual mapped tags and rating (including explicit overrides),
// not a later reconstruction from mutable category/rule state.
type approvedPolicyRow = acquisitionplan.ApprovedPolicyRow
type approvedPolicySnapshot = acquisitionplan.ApprovedPolicySnapshot
const maxPolicyProjectionBytes=acquisitionplan.MaxPolicyProjectionBytes
const maxPolicyProjectionRowBytes=acquisitionplan.MaxPolicyProjectionRowBytes

func policyRow(m *mapping.Mapper,row ManagedEvidencePost,native workerproto.MetadataPost)(approvedPolicyRow,error){
	if m==nil||row.Item.ID!=native.ID||row.Item.MediaURL!=native.MediaURL||len(native.Native)==0||
		mapping.PostID(row.Item.Meta)!=native.ID||m.ValidateSourceRating(row.Interpreted)!=nil{return approvedPolicyRow{},ErrManagedPreflight}
	pf:=m.Map(row.Interpreted)
	sum:=sha256.Sum256(native.Native)
	// Deep-copy the typed taxonomy by value, including nested alias slices.
	sourceTaxonomy, _:=row.Interpreted[kwdict.InterpretedTagsKey].([]kwdict.InterpretedTag)
	var taxonomy []acquisitionplan.ApprovedPolicyTag
	for _,t:=range sourceTaxonomy{
		taxonomy=append(taxonomy,acquisitionplan.ApprovedPolicyTag{Listed:t.Listed,Category:t.Category,Name:t.Name,
			Aliases:slices.Clone(t.Aliases),LegacyBlacklistName:t.LegacyBlacklistName})
	}
	p:=approvedPolicyRow{PostID:native.ID,NativeSHA256:hex.EncodeToString(sum[:]),MediaURL:native.MediaURL,
		SourceLabel:pf.Source,PostURL:m.PostURL(row.Interpreted),Rating:pf.Rating,
		Tags:slices.Clone(pf.Tags),WithheldTags:slices.Clone(pf.WithheldTags),
		WithheldCategories:slices.Clone(pf.WithheldCategories),UnknownTargets:slices.Clone(pf.UnknownTargets),Taxonomy:taxonomy,
		Provenance:&acquisitionplan.ApprovedProvenance{SourceMD5:kwdict.String(row.Item.Meta,"md5"),ParentURL:pf.ParentURL,
			Original:pf.Original,Commentary:pf.Commentary,CommentaryDText:pf.CommentaryDText,
			CommentaryTranslated:pf.CommentaryTranslated,CommentaryTranslatedDText:pf.CommentaryTranslatedDText,
			Notes:slices.Clone(pf.Notes),PostWidth:pf.PostWidth,PostHeight:pf.PostHeight,PostSize:pf.PostSize,
			PostExt:pf.PostExt,Collection:pf.Collection,CollectionOrder:pf.CollectionOrder}}
	body,err:=json.Marshal(p)
	if err!=nil||len(body)>maxPolicyProjectionRowBytes{return approvedPolicyRow{},ErrManagedPreflight}
	return p,nil
}
func encodeApprovedPolicy(q workerproto.TransferPlanRequest,rows []approvedPolicyRow)([]byte,string,error){
	body,digest,err:=acquisitionplan.EncodeApprovedPolicy(q,rows)
	if err!=nil{return nil,"",ErrManagedPreflight}
	return body,digest,nil
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
