# II.6.4c — Managed staging and durable outcome design

Subordinate technical design for the approved destination-aware staging model in [MEBOORU.md](../../MEBOORU.md). Initial scope is the strict acquisition candidate, not normal/deep acquisition, replacement, archive catch-up or production activation. No existing import route is replaced.

## Authority and identities

- A stable logical operation ID identifies an exact request across a lost response. It is not a pipe frame correlation ID, credential or proof of permission. The library must independently authorize the actual job, item/window, stable gallery identity, manifest, current policy and lifecycle; reject a reused ID with different scope. The initial typed request carries the exact strict transfer plan; it is a narrow first contract, not a grant for every acquisition family.
- Allocation mints a distinct opaque transfer ID under library ownership and records the exact request and owner-set resource limits. An exact repeated allocation must reconcile against durable allocation state, not create a second workspace. Initial candidate limits are consistent with the isolated strict verifier (512 MiB per file, 1 GiB total, no more files than the window); these are not production default changes. Quotas must ultimately be checked during writing, not only afterward.
- The worker must receive only a library-allocated workspace and capability, not choose a final destination. The initial allocation DTO intentionally does not encode a filesystem path: the later owner-side workspace resolver must bind a trusted destination root and exact allocation to the opaque ID. No arbitrary filesystem operation or caller-provided cleanup proof.
- Final authorization is independent of valid DTOs. All stage/grant/commit/status operations require library job/gallery authority; no route to bypass strict evidence, policy revocation, containment or network admission.

## Ordered implementation

1. Typed allocation and abandon contract, exact reply matching, no path/argv/quota or cleanup claim supplied by the requester; reserved operation dispatch stays disconnected.
2. Library-owned private staging root outside every scanned gallery, exact allocation and bounded resources. Verify actual roots and volume identity, refuse overlap, unavailable/read-only roots and ambiguous lifecycle. Prefer rooted `os.Root` operations; reject symlinks, traversal, unexpected hardlinks/special files and unlisted contents. Directory permissions are not a same-user sandbox.
3. After independently confirmed writer cleanup and strict result eligibility, copy to a library-owned sealed candidate on destination volume when needed. Verify size/hash and required native sidecar evidence again. No raw worker final path; never overwrite an existing file. Preserve existing duplicate/metadata policy by calling shared service logic at the appropriate point rather than duplicating ingestion.
4. Durable ledger records allocation, sealed evidence, commit intent and terminal receipt. Once commit intent is durable, an unknown acknowledgement must be reconciled with actual library DB/file identity before any retry or cleanup. Do not claim exactly-once transactions across separate databases or filesystems. Refuse abandonment while writers or commit outcome are uncertain. Receipt contains exact logical operation, transfer and stable gallery identity, media/item evidence and actual terminal outcome, including duplicate resolution.
5. Disposable cross-platform tests for symlinks/hardlinks/traversal, changed bytes, collision, failed copy/rename, low space/limits, cancellation, unavailable volume, gallery replacement, restart/lost reply and uncertain commit. Only after actual ownership and integrated acceptance may ordinary managed acquisition use this path.

## Existing code to preserve or adapt

`internal/gallery/ingest.go:Ingest/ingestWithHash` owns existing duplicate, alias-path, file facts, generation metadata and database behavior. `internal/gallery/replace.go` has existing replace-specific backup/restore and cross-volume copy semantics; its ordinary rename/copy is **not** a general no-clobber commit primitive, and strict replacement remains refused. `shared/fsx.WriteDurableIn` is rooted durable-file candidate (directory sync is best effort) for owner state, not no-clobber media publication or full cross-platform durability proof. `internal/acquisitionpolicy/custody_receipt.go` verifies a separate generation-bound custody-stop receipt; it is not an import receipt. Do not reuse it as proof of media commit. `shared/acquisitionplan.VerifyTransferResult` verifies point-in-time returned evidence under the original lease, not durable final identity. Separate media authentication limitations (source may mislabel or serve changed bytes at the same URL) remain explicit.

No product decision is requested here; these are incremental engineering constraints within the approved model.


## Initial rooted allocation candidate

The local-only allocator uses an existing dedicated owner-selected canonical staging directory and requires the caller's exclusive profile ownership. It checks all live gallery roots in both directions by actual ancestor identity; scope and bounds are supplied by trusted owner authority, not the wire. Exclusive operation directory contains canonical synced allocation.json and an opaque transfer-ID workspace. Exact reallocation/reopen returns the same reservation, never authorizes another launch; ambiguous entries block allocation and are retained. Candidate reservation cap 1024 entries / 16 GiB aggregate ceiling, selected lower by owner. No reclamation, live byte quota, durable gallery identity, destination volume qualification, power-loss directory durability or production binding yet. No filesystem mutation outside the dedicated staging root.


## Initial sealed-copy primitive

The strict runtime result now binds native sidecar bytes as well as media bytes. A local library-only copy helper accepts trusted manifest/result after independently proven cleanup; checks exact flat inventory and native evidence, canonical ID filename and supported extension, file count/byte limits including sidecars, rooted regular single-link objects; copies exclusively to a fresh owner root, fsyncs and independently rechecks all source/destination hashes. No caller-supplied path or arbitrary write from IPC. Failed copy retains incomplete destination without publishing success; durable state and actual custody binding remain later increments. A same-user adversary can still mutate files; this is not sandboxing or remote source authenticity.


## Persistent sealed evidence candidate

The allocator now has an isolated Seal/Sealed path. Only trusted local observer supplies exact original approved manifest and contained results after writer-stop proof; fixture observer is not production custody. Verify fixed strict workspace, exclusive `sealed` sibling, separately verified media copy, synced manifest and canonical seal.json. Partial marker retains all state and blocks replay/allocation; completed seal disallows workspace reopening and a second seal attempt. Reopen queries revalidate current gallery scope and actual bytes; no claim of final commit or permission to ingest, no automatic reclaim. Directory sync remains best effort. This does not solve external writer identity or power-loss atomicity.


## Destination volume and capacity candidate

Require actual opened stage/destination directory identities on the same device/volume (Unix device, Windows volume serial as point-in-time evidence). Unknown or different volume is refused in this first candidate; no cross-volume fallback until independently verified. Free-space preflight includes original plus sealed payload and maximum manifest/record overhead for each reservation, with a separate actual seal-copy preflight. Conservative accounting may double-count already-written bytes; no live quota, power-loss guarantee, durable volume ID or protection against other disk writers/mount changes. Existing exact replay does not require fresh free space or restart a writer. Tests exercise low and unknown space without mutation, aggregate reservations and low seal capacity before marker creation.


## No-clobber publication candidate

A third private prepared media copy preserves sealed evidence independently of eventual gallery bytes; initial reservation now includes three payload copies. Owner-only diagnostic prepare writes a synced exact intent, then publish writes a distinct synced attempt marker before rooted atomic no-clobber hard links into the gallery. No direct visible partial copy or overwrite fallback. Deterministic transfer-ID diagnostic names, not production naming behavior. Retain the private link until future receipt/cleanup; recovery requires exact file identity, two links and byte hash, not equal bytes alone. Query distinguishes prepared, absent after attempt, and placed; conflict/ambiguity refuses; never automatically retries after attempt. Gallery lifetime/scanner exclusion and actual DB metadata transactions are later mandatory integration gates; no production caller or durable DB receipt. Directory fsync best effort, unsupported link filesystems refuse, no hostile same-user guarantee.


## Database landing observation (not import receipt)

Existing `api/images.go:createImage` calls library Land/LandInPlace, then separate source/provenance/tag/relationship/collection operations. It can return errors after bytes or some rows are already committed. Do not infer completed import from a file, image row, HTTP status, or source membership alone. Candidate records exact synced landing attempt before any database mutation, binds stable gallery/grant and observed DB file identity, requires all file placements and absent path rows; read-only actual SQLite snapshot reconciles image/path/digest/size/canonical or alias relationship. Absent or observed are both nonterminal, never authorize automatic replay. Tests explicitly invoke actual legacy LandInPlace on disposable valid PNG with no watcher, including duplicate alias; no production caller and no full metadata application or receipt. Final managed upload duplicate handling, stable gallery/DB lifecycle and full metadata evidence must be integrated independently. SQLite currently uses WAL synchronous normal; do not claim power-loss durable multi-transaction receipt until qualified.


The contained strict manifest is necessary but not sufficient for import: verified alias taxonomy and mapped rating/tags/withheld rules must remain bound to the actual approved preflight. Capture local immutable bounded policy projection separately, with native digest/media ID and exact transfer identity; never reconstruct from a different rule snapshot or accept an untrusted worker-selected digest. Full source/provenance optional fields, job extras and library-side durable authorization are still separate work. Existing `gallery.MergeSource` and `ResolveTagNames` implement source ownership, rating fill and user rejection semantics; `LandInPlace` alone does not. Do not mistake an observed row for a receipt.


Shared approved policy artifact encoder/decoder requires independently trusted expected SHA256 and exact original native manifest, checks canonical encoding, bounded rows and strings, one-to-one post IDs/media URLs/source/subcategory and mapped rating/tag agreement. It does not prove semantic truth or approval; never derive expected digest from worker payload. The 64 MiB local artifact is not a single private frame. Library-side durable binding to actual custody and final import projection still outstanding.


Library-owned bound policy artifact: before publication, independently trusted local observer provides exact approved projection and expected digest; shared decoder verifies native manifest membership, returned IDs must be included. Store projection.json and binding.json (exact allocation, seal digest, projection digest) in exclusive private sealed/policy directory; incomplete binding blocks replay. Reopen rechecks original sealed evidence and bound artifact; no mutable re-mapping. Reserve additional maximum artifact bytes. Production must require actual custody and policy authority before binding/commit, not merely callback-provided digest. Diagnostic publication and landing tests remain disconnected. No final import receipt.


Mapped metadata candidate: after exact bound policy and landing observation, persist exclusive metadata-attempt.json with policy digest, exact target post/image IDs, preexisting rating and landing identity. Reuse existing MergeSource and tag service; verify actual source URL, resolved source tag attribution and effective rating; preserve existing rating and rejected/withheld tag behavior. Failure after one service commits retains attempt and refuses any replay. First candidate disallows same-image multi-post windows, unknown target warnings and unbound policy; no optional provenance or user extras, no duplicate-drop or final naming, no terminal receipt. Disposable gallery exclusion is assumed, not production fencing. Tests intentionally abort tag attribution after source commit to exercise partial failure.


Mapped-phase result is a bounded canonical owner record, tied to metadata attempt digest and per-post observed image ID, effective rating, rejections and resolved tag IDs. Reopen rechecks actual bound policy, landing, DB/file identity and source/tag/rating observations; missing record after attempt is uncertain, never replay. No terminal import or reclamation permission. Existing multi-transaction service semantics still require actual complete provenance/duplicate policy and final receipt design.


Approved source detail capture: preserve original mapped commentary/DText/translation, upstream/parent, notes, claimed MD5 and declared file facts/collection in immutable projection alongside native digest and measured transfer hashes (not equivalent). Explicit capture needed for eventual full commit; nil remains diagnostic fixture compatibility only. Apply existing normalization and provenance services in later increment; job extras and duplicate semantics not yet handled. Bounded record allowance increased to 8 MiB for phase records.


Shared gallery.NormalizeSourceInput preserves ordinary API normalization and is used by the managed mapped phase to apply exact approved source claims through existing MergeSource and ApplySourceProvenance. Read-only verification checks supplied fields/notes, not empty fields that preserve existing values. Source size and MD5 are claims, never measured local facts. Parent URL stored but graph relations, collections, job extras, actual duplicate-drop and final terminal receipt not yet integrated. No production dispatch.


## Narrow new-only terminal diagnostic

Exercise actual existing LandInPlace and mapped metadata services through durable one-attempt markers; refuse duplicate/missing-row SHA, preexisting source/parent references, missing provenance, parent/collection claims, non-PNG or partial approved window. Issue immutable new-receipt.json only for exact complete isolated new-only scope, after verification of actual file, DB, provenance, tags and exact single path/origin. Reopen verifies receipt against current evidence rather than authorizing replay. Not a production receipt, not a deletion grant; retain all evidence. Existing legacy behavior, duplicate drop/restore, relation handling, naming/job extras and real scanner exclusion still need qualification.


## Isolated receipt checkpoint

Hosted Linux/Windows and owner Apple Silicon Mac passed the corrected new-only receipt candidate including exact preexisting library-derived tag ownership (preserve legacy meta tags), source claims, active mapped attribution and actual read-only receipt recovery. See [native staging checkpoint](../validation/managed-staging-2026-11-27.md). This is a meaningful filesystem/hardware qualification boundary, not complete II.6.4c. No staging deletion until a separate verified reclamation state machine preserves exact terminal evidence and independently establishes cleanup authority. Do not implement a competing full metadata engine or silently change duplicate/restore behavior.


### Held duplicate disposition boundary

Existing HTTP multipart upload drops the landed duplicate; operator-owned path references retain aliases. Existing DropDuplicateCopy is best-effort and can log errors without returning them, so managed reconciliation must never infer terminal deletion from LandResult.Dropped. First isolated witness after mapped metadata observes exact noncanonical landed path and actual separately held canonical file (rooted path, regular single link, SHA, size and stable identity), with no mutation or deletion grant. Missing/reactivated/promoted canonicals, multi-post same-image windows, exact one-attempt duplicate disposal and receipt remain separate. Before deleting a managed alias, persist exact target and prove canonical remains available, independently observe DB/path removal and never touch operator-owned aliases. No automatic replay after ambiguous deletion.


Isolated single-held duplicate disposal records exact metadata result digest and separately held canonical identity before any unlink. Reobserve under current authority and rooted handles, remove only the operation-owned second link; retain all private bytes. Conditional deletion of exact noncanonical alias row follows successful unlink; independently confirm both absences, retained canonical hash/identity and current mapped metadata. A DB error after unlink is uncertain, never auto-repaired or replayed. Test operator-owned second copy stays intact, and revoked attempt cannot resume. This is a nonterminal disposition observation, not a production receipt or cleanup permission.


Restoration is distinct from held duplicate disposal: inherited Ingest reports moved/restored separately from ordinary duplicates. Isolated candidate binds prior image ID/path/missing flag/rating before actual LandInPlace, verifies new canonical and existing ID, preserves the marked-missing old path demotion vs unmarked promotion removal, refuses other aliases/unknown state, and never disposes restored bytes. Known single PNG with measured size only; not placeholder or general production restore. No replay or terminal receipt.


Relationship parity preparation: the HTTP parent/child linkage routine is now shared at library.Gallery.LinkParentRelations, preserving existing first-source/operator precedence and late parent arrival. Its best-effort logging is not terminal success evidence. A managed commit must distinguish no matching parent from lookup error, existing manual source from newly linked exact edge, and ambiguous URL matches/conflict; preserve legacy UI/API semantics and use existing relation service rather than writing an independent graph engine.


## Managed relationship phase candidate (2026-11-27)

**II.6.4c isolated managed relationship phase candidate:** exact single mapped post with explicit source details before duplicate disposal, bounded parent/child candidate and prior edge lookup, synced attempt tied to exact mapped result, current gallery/DB/root/authority checks, then existing shared LinkParentRelations; independently require expected graph and synced nonterminal outcome. Missing/self parent is legitimate; ambiguous URL, excessive candidates, lookup error, failed write, changed graph or authority loss refuse. Existing manual source retained. Exact named publication inventory replaces entry-count inference; unknown, orphan and malformed relationship records refuse; attempt-only remains inspectable, never replayable. Reserve both bounded relationship records. Fourteen disposable staging test modes; Windows targeted gate now explicitly runs internal/library too. No production activation or terminal receipts; no graph mutation after duplicate disposal, no multi-post ordering or durable production scanner exclusion proof. Hosted verification pending on code b263a27789c9f8067e934983ff82a249a823c686.

The source graph is read in a bounded snapshot; the legacy service still owns mutation and conflict/cycle rules. This is not a new graph engine. The candidate refuses any ambiguity instead of changing the legacy HTTP choice of lowest image ID. Recovery does not rerun a write, and missing outcome is not a retry grant. Existing new-only/duplicate/restoration diagnostics remain restricted; they do not yet require this relationship phase, and the observer currently requires placed bytes before disposal. Generalized receipt must compose all phases and retain an independent witness when the held alias has been removed. No new product behavior approved.

## Naming, collection and job extras — next integration obligations

Reviewed actual existing `internal/api/images.go:createImage`, `internal/library/land.go`, `internal/gallery/nametmpl.go`, `internal/gallery/collections.go` and `meloader/internal/pipeline/processor.go` on candidate 31c3f45. This is a source review, not tested implementation:

- Legacy multipart resolves explicit request folder literally; default upload folder with tokens is rendered after landing. Naming uses measured DB facts plus the submitted site/post ID, then `PlaceImage`. New and restored canonical files may be renamed; ordinary held duplicate upload is dropped and must not rename its retained canonical. Existing naming logs failures, so managed receipt needs exact path identity and observed disposition, not a successful call. A legitimate move invalidates current deterministic publication-path observation; design a durable intended/final path witness and prove no-clobber, no partial-visible copy, and recovery before calling that service. Do not weaken existing two-link checks merely to pass a rename.
- New import uses `ApplyCreateProvenance` -> `SetHomeCollection`; duplicate uses `AddCollectionMembership` so existing home remains unless the membership is the existing home. Existing label casing and position updates are meaningful. Derive exact membership and home outcome, persist attempt and independently observe; collection-order fallback is legacy item index + 1 when no source order is supplied. Bind that actual index before mutation. Source collection claims currently refused by the mapped diagnostic, not silently accepted.
- Job snapshot extras include folder, tags, commentary, source and via. `mapping.MergeExtras` distinguishes source-claimed labels from fallback direct-link labels; client tags are separately applied by library HTTP API. Preserve their separate ownership and rejection behavior; do not mix job extras into the immutable approved source tags or allow job input to bypass strict rating/blacklist decisions. Bind owner-approved job snapshot and current authorization independently of the worker. Strict replacement and unqualified normal/deep modes remain refused in managed path.

No production changes or new milestone acceptance in this checkpoint.
