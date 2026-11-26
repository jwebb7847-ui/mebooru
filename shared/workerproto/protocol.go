// Package workerproto defines the private library/worker protocol vocabulary.
// It does not authorize or dispatch operations, open channels, or enable WAN.
package workerproto

import "encoding/json"

const (
	Version uint16 = 1
	MaxControlBytes = 16 << 20
)

type Role string

const (
	Library Role = "library"
	Worker Role = "worker"
	OperationRequester Role = "operation-requester"
	OperationCustodian Role = "operation-custodian"
)

type Kind string

const (
	Request Kind = "request"
	Response Kind = "response"
	Cancel Kind = "cancel"
	Event Kind = "event"
)

// ID is scoped to the initiating endpoint and connection. It is not a job
// ID or an idempotency key. Payloads are decoded into operation-specific DTOs.
type Envelope struct {
	Kind Kind `json:"kind"`
	ID uint64 `json:"id,omitempty"`
	Op Operation `json:"op,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error *Fault `json:"error,omitempty"`
}

type Fault struct {
	Code string `json:"code"`
	Message string `json:"message,omitempty"`
}

type Hello struct {
	Version uint16 `json:"version"`
	Role Role `json:"role"`
}

type OfflinePolicy struct {
	Offline bool `json:"offline"`
}

type JobRef struct {
	JobID int64 `json:"job_id"`
}

type ImageRef struct {
	Gallery string `json:"gallery"`
	ImageID int64 `json:"image_id"`
}

type Operation string
type NetworkPolicy string

const (
	// Local means the operation itself must not initiate WAN work.
	Local NetworkPolicy = "local"
	// Online means the operation requires an active online policy.
	Online NetworkPolicy = "online"
	// Mixed requires per-path checks; it never grants WAN access by itself.
	Mixed NetworkPolicy = "mixed"
)

type Rule struct {
	Caller Role
	Network NetworkPolicy
}

const (
	// Separate private channel; no library/worker role can invoke these.
	OperationProbeStart Operation = "custody.probe_start"
	OperationProbeStop Operation = "custody.probe_stop"

	Health Operation = "worker.health"
	SetOffline Operation = "worker.set_offline"
	Shutdown Operation = "worker.shutdown"
	ReserveIdle Operation = "worker.reserve_idle"

	QueueList Operation = "queue.list"
	QueueGet Operation = "queue.get"
	QueueEnqueue Operation = "queue.enqueue"
	QueueRetry Operation = "queue.retry"
	QueueContinue Operation = "queue.continue"
	QueueContinueAll Operation = "queue.continue_all"
	QueueCancel Operation = "queue.cancel"
	QueuePause Operation = "queue.pause"
	QueueResume Operation = "queue.resume"
	QueueClear Operation = "queue.clear"
	QueueClearSucceeded Operation = "queue.clear_succeeded"
	QueueCancelPending Operation = "queue.cancel_pending"
	QueueRemoveSeries Operation = "queue.remove_series"

	WatchList Operation = "watch.list"
	WatchAdd Operation = "watch.add"
	WatchSetInterval Operation = "watch.set_interval"
	WatchSetItems Operation = "watch.set_items"
	WatchCheck Operation = "watch.check"
	WatchRemove Operation = "watch.remove"
	WatchFromJob Operation = "watch.from_job"

	MetadataFetch Operation = "metadata.fetch"
	MediaReplace Operation = "media.replace"
	Lookup Operation = "lookup.enqueue"
	GalleryRenamed Operation = "gallery.renamed"
	PTRStatus Operation = "ptr.status"
	PTRLookup Operation = "ptr.lookup"
	PTRTags Operation = "ptr.tags"
	PTRSearch Operation = "ptr.search"
	PTRPreview Operation = "ptr.preview"
	PTRPairPreview Operation = "ptr.pair_preview"
	PTRContribute Operation = "ptr.contribute"
	PTRSync Operation = "ptr.sync"
	PTRAccountCreate Operation = "ptr.account_create"
	PTRContribRetry Operation = "ptr.contrib_retry"
	PTRContribRescind Operation = "ptr.contrib_rescind"
	PTRContribLog Operation = "ptr.contrib_log"
	PTRDeleteLocal Operation = "ptr.delete_local"

	RulesLookup Operation = "rules.lookup"
	SettingsRead Operation = "settings.read"
	SettingsUpdate Operation = "settings.update"
	SiteCatalog Operation = "site.catalog"
	SiteOptions Operation = "site.options"
	SiteTest Operation = "site.test"
	LookupTest Operation = "lookup.test"
	TracePost Operation = "site.trace"
	RuntimeStatus Operation = "runtime.status"
	RuntimeInstall Operation = "runtime.install"
	RuntimeRevert Operation = "runtime.revert"

	GalleryList Operation = "library.galleries"
	CategoryList Operation = "library.categories"
	ResolveTags Operation = "library.resolve_tags"
	ImageDigest Operation = "library.image_digest"
	ImageThumbnail Operation = "library.image_thumbnail"
	StageAllocate Operation = "library.stage_allocate"
	StageAbandon Operation = "library.stage_abandon"
	ImportMedia Operation = "library.import_media"
	ReplaceMedia Operation = "library.replace_media"
	EnrichMedia Operation = "library.enrich_media"
	FetchOutcome Operation = "library.fetch_outcome"
)

// Rules are metadata, not authorization. The eventual dispatcher must also
// enforce session/role, offline state, consent, job scope and typed validation.
// Enqueue and resume may persist held work offline; scheduling/transfer must
// never start WAN as a side effect until separately authorized.
var rules = map[Operation]Rule{
	OperationProbeStart: {OperationRequester, Local},
	OperationProbeStop: {OperationRequester, Local},
	Health: {Library, Local},
	SetOffline: {Library, Local},
	Shutdown: {Library, Local},
	ReserveIdle: {Library, Local},
	QueueList: {Library, Local},
	QueueGet: {Library, Local},
	QueueEnqueue: {Library, Mixed},
	QueueRetry: {Library, Mixed},
	QueueContinue: {Library, Mixed},
	QueueContinueAll: {Library, Mixed},
	QueueCancel: {Library, Local},
	QueuePause: {Library, Local},
	QueueResume: {Library, Mixed},
	QueueClear: {Library, Local},
	QueueClearSucceeded: {Library, Local},
	QueueCancelPending: {Library, Local},
	QueueRemoveSeries: {Library, Local},
	WatchList: {Library, Local},
	WatchAdd: {Library, Mixed},
	WatchSetInterval: {Library, Mixed},
	WatchSetItems: {Library, Mixed},
	WatchCheck: {Library, Mixed},
	WatchRemove: {Library, Local},
	WatchFromJob: {Library, Mixed},
	MetadataFetch: {Library, Mixed},
	MediaReplace: {Library, Mixed},
	Lookup: {Library, Mixed},
	GalleryRenamed: {Library, Local},
	PTRStatus: {Library, Local},
	PTRLookup: {Library, Mixed},
	PTRTags: {Library, Mixed},
	PTRSearch: {Library, Mixed},
	PTRPreview: {Library, Mixed},
	PTRPairPreview: {Library, Mixed},
	PTRContribute: {Library, Online},
	PTRSync: {Library, Mixed},
	PTRAccountCreate: {Library, Online},
	PTRContribRetry: {Library, Online},
	PTRContribRescind: {Library, Mixed},
	PTRContribLog: {Library, Local},
	PTRDeleteLocal: {Library, Local},
	RulesLookup: {Library, Local},
	SettingsRead: {Library, Local},
	SettingsUpdate: {Library, Mixed},
	SiteCatalog: {Library, Mixed},
	SiteOptions: {Library, Mixed},
	SiteTest: {Library, Online},
	LookupTest: {Library, Online},
	TracePost: {Library, Online},
	RuntimeStatus: {Library, Local},
	RuntimeInstall: {Library, Online},
	RuntimeRevert: {Library, Local},
	GalleryList: {Worker, Local},
	CategoryList: {Worker, Local},
	ResolveTags: {Worker, Local},
	ImageDigest: {Worker, Local},
	ImageThumbnail: {Worker, Local},
	StageAllocate: {Worker, Local},
	StageAbandon: {Worker, Local},
	ImportMedia: {Worker, Local},
	ReplaceMedia: {Worker, Local},
	EnrichMedia: {Worker, Local},
	FetchOutcome: {Worker, Local},
}

func RuleFor(op Operation) (Rule, bool) {
	rule, ok := rules[op]
	return rule, ok
}

// AllowedDirection only checks the vocabulary. It is never sufficient to
// authorize an operation or its payload.
func AllowedDirection(caller Role, op Operation) bool {
	rule, ok := RuleFor(op)
	return ok && rule.Caller == caller
}

// counterpart constrains both handshake and request dispatch to the
// same role pair. A worker/library session cannot become a broker.
func counterpart(role Role) Role {
	switch role {
	case Library:return Worker
	case Worker:return Library
	case OperationRequester:return OperationCustodian
	case OperationCustodian:return OperationRequester
	default:return ""
	}
}
