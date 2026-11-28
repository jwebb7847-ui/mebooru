package acquisitionstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/contract"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/library"
)

// A durable prepublication decision, not yet a publication attempt.
// It is deliberately mutually exclusive with diagnostic/new-only
// publication and landing records. No executor can consume it yet.
// A future version must bind actual owner duplicate policy, naming and
// downstream writes; this callback is an isolated owner fixture only.
const existingTargetPlanName = "existing-target-plan.json"
const existingTargetAnchorName = "existing-target-anchor"
const maxExistingTargetAnchorBytes = 64

type existingDispositionAuthority func(context.Context, workerproto.StageAllocation) (string, error)

type existingTargetPlan struct {
	Version int
	Target existingPublicationTarget
}

func validExistingTargetPlan(m existingTargetPlan, r record, f acquisitionplan.VerifiedTransferFile) bool {
	v := m.Target
	db := landingAttempt{Version:1, Allocation:r.Allocation, GalleryIdentity:r.GalleryIdentity, DBPath:v.DB.DBPath, DBFileIdentity:v.DB.DBFileIdentity}
	if m.Version != 1 || v.DB != db || !filepath.IsAbs(db.DBPath) || filepath.Clean(db.DBPath) != db.DBPath ||
		db.DBFileIdentity == "" || len(db.DBFileIdentity) > 128 || v.File != f || !validResultFile(f) ||
		filepath.Ext(f.RelativeName) != ".png" || !validPlannedImageFacts(f,&v.Media) ||
		!validDigest(v.PolicySHA256) || !validDigest(v.StateSHA256) || v.ImageID <= 0 ||
		v.FileIdentity == "" || len(v.FileIdentity) > 128 {
		return false
	}
	managed := finalName(r.Allocation,f)
	switch v.Kind {
	case existingHeld:
		if v.Held == nil || v.Restore != nil || len(v.Retained) == 0 || len(v.Retained) > maxHeldAliases { return false }
		h := v.Held
		if h.ImageID != v.ImageID || h.PostID != f.ID || h.Size != f.Size || h.SHA256 != f.SHA256 ||
			!validNamingRelative(h.CanonicalPath) || strings.EqualFold(h.CanonicalPath,managed) ||
			h.CanonicalIdentity == "" || len(h.CanonicalIdentity)>128 || h.CanonicalIdentity==v.FileIdentity { return false }
		canon := 0
		seen := map[string]bool{}
		for i,p := range v.Retained {
			if p.ID<=0 || (i>0 && p.ID<=v.Retained[i-1].ID) || !validNamingRelative(p.Relative) ||
				strings.EqualFold(p.Relative,managed) || p.Identity=="" || len(p.Identity)>128 ||
				p.Identity==v.FileIdentity || seen[p.Identity] { return false }
			seen[p.Identity]=true
			if p.Canonical {
				canon++
				if p.Relative!=h.CanonicalPath || p.Identity!=h.CanonicalIdentity { return false }
			}
		}
		return canon==1
	case existingRestore:
		if v.Held!=nil || v.Retained!=nil || v.Restore==nil { return false }
		x:=*v.Restore
		if x.RestoreImageID!=v.ImageID || x.RestorePathID<=0 || !validDigest(x.RestorePreservationSHA256) ||
			!canonicalRating(x.RestoreRating) || !validNamingRelative(x.RestorePath) ||
			filepath.Base(x.RestorePath)!=x.RestorePath || strings.EqualFold(x.RestorePath,managed) { return false }
		x.RestoreImageID=0; x.RestorePathID=0; x.RestorePreservationSHA256=""
		x.RestoreRating=""; x.RestorePath=""; x.RestoreMarkedMissing=false
		return x==db
	}
	return false
}

func encodeExistingTargetPlan(m existingTargetPlan,r record,f acquisitionplan.VerifiedTransferFile)([]byte,error){
	if !validExistingTargetPlan(m,r,f){return nil,ErrStageRefused}
	raw,err:=json.Marshal(m)
	if err!=nil||len(raw)>maxPlannedAttemptBytes{return nil,ErrStageLimit}
	return raw,nil
}

func readExistingTargetPlan(p *os.Root,r record,f acquisitionplan.VerifiedTransferFile)(existingTargetPlan,error){
	var m existingTargetPlan
	raw,err:=boundedRegularBytes(p,existingTargetPlanName,maxPlannedAttemptBytes)
	if err!=nil||json.Unmarshal(raw,&m)!=nil{return existingTargetPlan{},ErrStageRecovery}
	anchor,err:=boundedRegularBytes(p,existingTargetAnchorName,maxExistingTargetAnchorBytes)
	if err!=nil||string(anchor)!=digestBytes(raw){return existingTargetPlan{},ErrStageRecovery}
	canonical,err:=encodeExistingTargetPlan(m,r,f)
	if err!=nil||!bytes.Equal(raw,canonical){return existingTargetPlan{},ErrStageRecovery}
	return m,nil
}

// Couple the explicit disposition decision to every fresh job-authority
// read used by the existing double-observation primitive. Never fall
// back to the alternate disposition, even if its target would qualify.
func existingPlanAuthority(kind string,disposition existingDispositionAuthority,extras extrasAuthority) extrasAuthority {
	if disposition==nil||extras==nil{return nil}
	return func(ctx context.Context,grant workerproto.StageAllocation)(contract.JobExtras,error){
		got,err:=disposition(ctx,grant)
		if err!=nil||got!=kind{return contract.JobExtras{},ErrStageRefused}
		return extras(ctx,grant)
	}
}

func(a *Allocator) recordExistingTargetPlan(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,expected existingPublicationTarget,disposition existingDispositionAuthority,extras extrasAuthority)error{
	a.mu.Lock();defer a.mu.Unlock()
	authority:=existingPlanAuthority(expected.Kind,disposition,extras)
	current,err:=a.inspectExistingPublicationTargetLocked(ctx,grant,g,expected.Kind,authority)
	if err!=nil||!sameExistingPublicationTarget(expected,current){return ErrStageRefused}
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return ErrStageRefused}
	for _,name:=range []string{existingTargetPlanName,existingTargetAnchorName}{
		if _,err:=p.Lstat(name);!errors.Is(err,os.ErrNotExist){return ErrStageRecovery}
	}
	m:=existingTargetPlan{Version:1,Target:current}
	raw,err:=encodeExistingTargetPlan(m,r,e.Files[0])
	if err!=nil{return err}
	scope,err:=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,scope)||!a.livePublicationRoots(grant,r,p,src,dst){return ErrStageRefused}
	// Record the independent digest anchor first. A crash between the
	// two exclusive writes leaves a refused partial plan, never permission
	// to proceed with an older publication path. Loss of either record
	// alone is detectable; this is not a same-user tamper-proof ledger.
	if err:=writeOwnerBytes(ctx,p,existingTargetAnchorName,[]byte(digestBytes(raw)));err!=nil{return err}
	syncDirectory(p)
	scope,err=a.scope(ctx,grant.Request)
	if err!=nil||!matches(r,scope)||!a.livePublicationRoots(grant,r,p,src,dst){return ErrStageRefused}
	if err:=writeOwnerBytes(ctx,p,existingTargetPlanName,raw);err!=nil{return err}
	syncDirectory(p)
	// Once persisted, refuse replay or overwrites even if acknowledgement
	// is lost. Re-observation, not a second write, resolves that state.
	observed,err:=a.observeExistingTargetPlanLocked(ctx,grant,g,disposition,extras)
	if err!=nil||!sameExistingPublicationTarget(observed.Target,current){return ErrStageRecovery}
	return nil
}

func(a *Allocator) observeExistingTargetPlan(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority)(existingTargetPlan,error){
	a.mu.Lock();defer a.mu.Unlock()
	return a.observeExistingTargetPlanLocked(ctx,grant,g,disposition,extras)
}

func(a *Allocator) observeExistingTargetPlanLocked(ctx context.Context,grant workerproto.StageAllocation,g *library.Gallery,disposition existingDispositionAuthority,extras extrasAuthority)(existingTargetPlan,error){
	var zero existingTargetPlan
	r,e,p,src,dst,err:=a.publicationRoots(ctx,grant)
	if err!=nil{return zero,err}
	defer p.Close();defer src.Close();defer dst.Close()
	if len(e.Files)!=1{return zero,ErrStageRecovery}
	stored,err:=readExistingTargetPlan(p,r,e.Files[0])
	if err!=nil{return zero,err}
	current,err:=a.inspectExistingPublicationTargetLocked(ctx,grant,g,stored.Target.Kind,existingPlanAuthority(stored.Target.Kind,disposition,extras))
	if err!=nil||!sameExistingPublicationTarget(stored.Target,current){return zero,ErrStageRefused}
	again,err:=readExistingTargetPlan(p,r,e.Files[0])
	if err!=nil||!sameExistingPublicationTarget(stored.Target,again.Target)||!a.livePublicationRoots(grant,r,p,src,dst){return zero,ErrStageRecovery}
	return stored,nil
}
