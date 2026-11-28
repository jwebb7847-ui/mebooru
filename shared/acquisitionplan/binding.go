package acquisitionplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

// StrictQualifiedVersion is the existing strict predicate qualification,
// not release authentication. Changing it requires source/transfer evidence.
const StrictQualifiedVersion = "1.32.19"

// RuntimeIdentity is an independently established custody-side observation,
// never a worker DTO. The artifact and dependency manifest digests must
// come from a trusted installation record and fresh verification, not
// a diagnostic fixture or version string. This type compares identities;
// it does not establish the authenticity of either record.
type RuntimeIdentity struct {
	Generation uint64
	Version string
	ArtifactSHA256 string
	ManifestSHA256 string
}

func validDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s && s != strings.Repeat("0", 64)
}

func (r RuntimeIdentity) valid() bool {
	return r.Generation != 0 && r.Version == StrictQualifiedVersion &&
		validDigest(r.ArtifactSHA256) && validDigest(r.ManifestSHA256)
}

// MetadataConfig is a fixed, credential-free candidate profile. No raw
// config, site overrides, archive, cache path, or arbitrary option strings.
// It is not yet a qualified replacement for a user's existing settings.
// Trusted policy code must refuse incompatible settings before selecting it.
type MetadataConfig struct {
	Generation uint64
	Source string
}

func (c MetadataConfig) args() ([]string, error) {
	if c.Generation == 0 { return nil, ErrMetadataPlan }
	args := []string{"-o", "timeout=20", "-o", "retries=2"}
	switch c.Source {
	case "derpibooru":
	case "danbooru":
		args = append(args, "-o", "extractor.danbooru.metadata=false")
	case "e621":
		args = append(args, "-o", "extractor.e621.metadata=false")
	default:
		return nil, ErrMetadataPlan
	}
	return args, nil
}

// ConfigIdentity fingerprints the exact generated argument policy, not a
// mutable file. The generation separately invalidates prior permissions
// even when the generated options happen to be identical.
type ConfigIdentity struct {
	Generation uint64
	SHA256 string
}

func (c MetadataConfig) Identity() (ConfigIdentity, error) {
	args, err := c.args()
	if err != nil { return ConfigIdentity{}, err }
	// Domain-separate the profile from runtime or arbitrary file digests.
	body, err := json.Marshal([]any{"metadata-config-v1", c.Source, args})
	if err != nil { return ConfigIdentity{}, ErrMetadataPlan }
	digest := sha256.Sum256(body)
	return ConfigIdentity{Generation:c.Generation, SHA256:hex.EncodeToString(digest[:])}, nil
}

type MetadataIdentity struct {
	Runtime RuntimeIdentity
	Config ConfigIdentity
}

type sequencePhase uint8

const (
	awaitClassification sequencePhase = iota
	classifying
	classified
	consumed
	refused
)

// MetadataSequence is a one-use, in-memory candidate binding. Construct
// only from a trusted library/custodian grant. It is not a job ledger,
// network lease, durable identity, or executable dispatcher. Cancellation,
// ambiguous completion, offline transition and generation change must
// invalidate it. No method starts a process.
type MetadataSequence struct {
	mu sync.Mutex
	phase sequencePhase
	resolve workerproto.MetadataPlanRequest
	classify workerproto.MetadataPlanRequest
	config MetadataConfig
	identity MetadataIdentity
}

func NewMetadataSequence(resolve workerproto.MetadataPlanRequest, runtime RuntimeIdentity, config MetadataConfig) (*MetadataSequence, error) {
	if !workerproto.ValidMetadataPlan(resolve) || resolve.Kind != workerproto.MetadataStrictResolve ||
		!runtime.valid() || config.Source != resolve.Source {
		return nil, ErrMetadataPlan
	}
	id, err := config.Identity()
	if err != nil { return nil, err }
	classify := resolve
	classify.Kind = workerproto.MetadataClassify
	classify.First, classify.Last = 0, 0
	return &MetadataSequence{resolve:resolve, classify:classify, config:config,
		identity:MetadataIdentity{Runtime:runtime, Config:id}}, nil
}

func (s *MetadataSequence) compile(expected, request workerproto.MetadataPlanRequest) ([]string, error) {
	return s.config.Compile(expected,request)
}

// Compile reconstructs the qualified command at the contained runtime
// entry point. The independently claimed job supplies expected.
func (c MetadataConfig) Compile(expected, request workerproto.MetadataPlanRequest) ([]string,error) {
	if expected.Source!=c.Source{return nil,ErrMetadataPlan}
	args, err := CompileMetadata(expected, request)
	if err != nil { return nil, err }
	options, err := c.args()
	if err != nil { return nil, err }
	// Only generated fixed options, immediately after --config-ignore.
	out := append([]string{args[0]}, options...)
	return append(out, args[1:]...), nil
}

func (s *MetadataSequence) BeginClassification(request workerproto.MetadataPlanRequest, observed MetadataIdentity) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != awaitClassification || s.identity != observed || s.classify != request {
		s.phase = refused
		return nil, ErrMetadataPlan
	}
	args, err := s.compile(s.classify, request)
	if err != nil { s.phase = refused; return nil, err }
	s.phase = classifying
	return args, nil
}

// ClassificationCompletion must originate from the custodian's own exact
// contained operation, never from worker JSON. Cleaned is true only after
// independent cleanup proof; this struct cannot supply that proof itself.
type ClassificationCompletion struct {
	Request workerproto.MetadataPlanRequest
	Observed MetadataIdentity
	ExitCode int
	Cleaned bool
	Stdout string
	Stderr string
}

const maxClassificationBytes = 64 << 10

// Require one unambiguous header/value pair. Extra extractor blocks,
// unexpected sites and unqualified subcategories cannot authorize resolve.
// QualifiedClassification returns the exact qualified extractor subcategory.
func QualifiedClassification(output, source string)(string,error){
	if len(output)>maxClassificationBytes{return "",ErrMetadataPlan}
	lines:=strings.Split(strings.ReplaceAll(output,"\r\n","\n"),"\n")
	var result string
	for i,line:=range lines{
		if !strings.Contains(line,"Category / Subcategory"){continue}
		if result!=""||i+1>=len(lines){return "",ErrMetadataPlan}
		parts:=strings.Split(strings.TrimSpace(lines[i+1])," / ")
		if len(parts)!=3{return "",ErrMetadataPlan}
		site:=strings.Trim(strings.TrimSpace(parts[0]),"\"")
		sub:=strings.Trim(strings.TrimSpace(parts[1]),"\"")
		if site!=source||!StrictSource(site,sub){return "",ErrMetadataPlan}
		result=sub
	}
	if result==""{return "",ErrMetadataPlan}
	return result,nil
}

func (s *MetadataSequence) AcceptClassification(result ClassificationCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_,classificationErr:=QualifiedClassification(result.Stdout,s.resolve.Source)
	if s.phase != classifying || s.classify != result.Request || s.identity != result.Observed ||
		result.ExitCode != 0 || !result.Cleaned || result.Stderr != "" ||
		classificationErr!=nil {
		s.phase = refused
		return ErrMetadataPlan
	}
	s.phase = classified
	return nil
}

func (s *MetadataSequence) BeginResolve(request workerproto.MetadataPlanRequest, observed MetadataIdentity) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != classified || s.identity != observed || s.resolve != request {
		s.phase = refused
		return nil, ErrMetadataPlan
	}
	args, err := s.compile(s.resolve, request)
	if err != nil { s.phase = refused; return nil, err }
	s.phase = consumed
	return args, nil
}

func (s *MetadataSequence) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = refused
}
