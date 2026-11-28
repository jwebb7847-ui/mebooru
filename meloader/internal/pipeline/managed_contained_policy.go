package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/mapping"
)

func nativeDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// EvaluateContainedPolicy runs in the custodian's supervised runtime
// child, using the same strict matcher and taxonomy as the worker.
func EvaluateContainedPolicy(ctx context.Context, q workerproto.MetadataPlanRequest, sub string, raw []byte,
	mapper *mapping.Mapper, transport http.RoundTripper, cfg windowregistry.LiveConfig, online func() bool) (windowregistry.LivePolicy, error) {
	gate := workerproto.NewDrainGate(ctx)
	if err := gate.Open(); err != nil {
		return windowregistry.LivePolicy{}, err
	}
	options, id := cfg.Identity(q.Source)
	scope := func(c context.Context, got workerproto.MetadataPlanRequest) bool {
		return c.Err() == nil && got == q && online()
	}
	w, err := newManagedEvidenceWindow(q, sub, raw, gate, gate.CurrentGeneration(), scope, mapper, transport)
	if err != nil {
		return windowregistry.LivePolicy{}, err
	}
	p, err := newManagedPreflight(w, id, options, 1, func(context.Context) (acquisitionplan.MetadataIdentity, uint64, error) { return id, 1, nil })
	if err != nil {
		return windowregistry.LivePolicy{}, err
	}
	result, err := p.Run(ctx, q)
	if err != nil {
		return windowregistry.LivePolicy{}, err
	}
	pol := windowregistry.LivePolicy{Request: q, Subcategory: sub, NativeSHA256: nativeDigest(raw), Listed: result.ListedIDs, Blocked: result.BlockedIDs}
	if a := result.Approval; a != nil {
		body, digest, err := a.policyEvidence(ctx, a.Request())
		if err != nil {
			return windowregistry.LivePolicy{}, err
		}
		pol.Proposal = workerproto.WindowAllocateProposal{Transfer: a.Request(), Projection: body, PolicySHA256: digest}
	}
	return pol, nil
}

// Bind the custodian verdict to this worker's independently completed
// metadata stream and live admission generation.
func bindContainedPolicy(ctx context.Context, completed *processguard.MetadataCompletion, r *ManagedWindowRuntime,
	gate *workerproto.DrainGate) (ManagedPreflightResult, error) {
	q := r.Request
	sub, raw, generation, err := completed.Consume(ctx, q, r.Identity, gate)
	if err != nil {
		return ManagedPreflightResult{}, err
	}
	pol, err := r.ContainedPolicy(ctx)
	if err != nil || pol.Request != q || pol.Subcategory != sub || pol.NativeSHA256 != nativeDigest(raw) {
		return ManagedPreflightResult{}, ErrManagedPreflight
	}
	posts, err := workerproto.DecodeMetadataEvidence(raw, q, sub)
	if err != nil || len(posts) != len(pol.Listed) {
		return ManagedPreflightResult{}, ErrManagedPreflight
	}
	ids := make([]string, 0, len(posts))
	blocked := make([]string, 0, len(posts))
	for i, post := range posts {
		if post.ID != pol.Listed[i] {
			return ManagedPreflightResult{}, ErrManagedPreflight
		}
		if slices.Contains(pol.Blocked, post.ID) {
			blocked = append(blocked, post.ID)
		} else {
			ids = append(ids, post.ID)
		}
	}
	if !slices.Equal(blocked, pol.Blocked) {
		return ManagedPreflightResult{}, ErrManagedPreflight
	}
	result := ManagedPreflightResult{ListedIDs: pol.Listed, BlockedIDs: blocked}
	if len(ids) == 0 {
		if len(pol.Proposal.Projection) != 0 {
			return ManagedPreflightResult{}, ErrManagedPreflight
		}
		return result, nil
	}
	plan, err := acquisitionplan.NewStrictTransferPlan(q, sub, posts, ids, r.PolicyGeneration, r.Identity, r.Config)
	if err != nil || plan.Request() != pol.Proposal.Transfer {
		return ManagedPreflightResult{}, ErrManagedPreflight
	}
	w := &ManagedEvidenceWindow{grant: q, gate: gate, generation: generation, scope: r.Scope}
	p := &ManagedPreflight{window: w, identity: r.Identity, config: r.Config, policyGeneration: r.PolicyGeneration, observe: r.Observe}
	if !p.current(ctx) {
		return ManagedPreflightResult{}, ErrManagedPreflight
	}
	result.Approval = &ManagedTransferApproval{preflight: p, plan: plan, projection: pol.Proposal.Projection, projectionSHA256: pol.Proposal.PolicySHA256}
	return result, nil
}
