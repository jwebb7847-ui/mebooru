# Managed worker — technical map at II.6.4a acceptance

Snapshot 2026-11-26, accepted code `2d0b122326fd504eb54cedf575d55cb03348c479` in draft [PR #38](https://github.com/jwebb7847-ui/mebooru/pull/38). [North star](../../MEBOORU.md) is the sole roadmap. [Dated evidence](../validation/worker-ownership-runtime-2026-11-26.md) records actual platform results and preserved history. This map is a code-navigation and review reference, not another roadmap or production enablement instruction.

Current development extension (2026-11-27): b isolated adapters verified on f7e27065e6d9ec81697324c7d5b59691514f8716 (run 45943329373); this historical acceptance map retains the a baseline. See [operation inventory](managed-operation-inventory.md) and [staging technical design](managed-staging.md) for current candidate scope and remaining activation requirements.

## Architecture and ownership

```text
Library (profile owner, persisted consent, gallery DB/media)
    | private inherited typed session
    v
Meloader worker (queue/source state; admission and drain leases)
    | separate private requester/custodian session
    v
Transient cleanup custodian (outside owned process boundaries)
    | register dormant exact handle, then activate
    +-- primary worker group / Job
    +-- independent operation group / Job A
    +-- independent operation group / Job B
    +-- generation-specific receipt after all cleanup proven
```

The diagram shows logical control and custody, not a literal process parent tree. The library starts the custodian; the custodian owns the primary worker and independent operations. No persistent service, third-party public execution API, or new production listener. Existing browser library UI and transitional first-party HTTP worker path remain usable until migration acceptance. Actual production launch still uses legacy paths; diagnostic modes never initialize ordinary queue/config/gallery state. Local version inspection is not online consent.

| Code | Responsibility and invariant |
| --- | --- |
| `internal/acquisitionpolicy/lock.go`, `fence.go`, `custody_receipt.go` | Exclusive rooted profile lock on stable inode; pending/armed durable generation, clean only on exact receipt; legacy dirty/malformed/stale receipt fail closed. Never unlink lock to recover. |
| `internal/acquisitionpolicy/store.go`, `coordinator.go` | Missing or invalid record denies; close library admission before policy change; persist before granting online; save/apply failure revokes and cleans up, report persistence errors. |
| `internal/acquisitionpolicy/manager.go`, `session_peer.go` | Demand/lease ownership, safe idle reservation, bounded restart cooldown, validated lifecycle acknowledgements; no scheduler or implicit retry. |
| `internal/acquisitionpolicy/custodian.go`, `custody_process.go`, `custody_session.go` | Trusted dormant startup, separate readiness/liveness/activation and protocol, independent shutdown deadline, receipt only after combined proof. |
| `shared/processguard/supervisor_unix.go`, `guard_unix.go`, `group_unix.go`; Windows supervisor/Job files | Owned guard/group or Job assigned before activation, no stale-ID kill. Unix ESRCH only (Darwin transient EPERM retry up to 3s); Windows zero membership plus reaping. Deliberately detached Unix descendants and simultaneous sentinel loss not proven. |
| `shared/processguard/operation_set.go`, `probe_broker.go`, `stream_probe.go` | Register before activation; exact lease Stop, retained uncertainty, bounded per-generation IDs, seal before snapshot; local fixed probe commands only. |
| `shared/workerproto/protocol.go`, `session.go`, `frame.go` | Role/version/direction checks, strict JSON, bounded framing/correlation/cancel, separate channels; registry is not authorization. |
| `shared/workerproto/drain_gate.go`, `probe.go` | Tracked leases; cancel is not completion. Drain closes admission then awaits proof; failed cleanup poisons; Seal permanent. Actual fixed transfer diagnostic retains lease beyond start RPC. |
| `shared/workerproto/stream_probe.go`, `catalog_probe.go`, `version_probe.go`, `probe_client.go` | Strict fixed diagnostic DTOs, exact byte cursor, 32 KiB chunks, 64 MiB cumulative diagnostic cap, 4 KiB stderr, distinct EOF/result/proof. Known canceled read uses exact Stop; ambiguity in start/finish/stop revokes diagnostic session. |
| `cmd/monbooru/managed_probe*.go`, `managed_runtime_probe.go`, `managed_stream_probe.go`, `managed_transfer_probe.go`, `managed_transfer_drain_probe.go`; `meloader/cmd/monloader/managed_probe.go` | Actual executable-only disposable diagnostics; root creates temp profile; runtime checks fixed sibling/digest, controlled local command or synthetic loopback fixture. No raw caller path/argv/env. |
| `meloader/internal/gdl/executor.go`, `gdl.go`, `strict.go` | Internal capture/stream execution seam; strict version and preflight use selected executor; status/cleanup separate. Proof-required mode rejects legacy launcher and direct HTTP fallback. Legacy mode unchanged; managed production constructor/adapter absent. |
| `internal/api/managed_catalog.go`, `shared/workerproto/catalog.go`, `meloader/internal/monbooru/managed_catalog.go` | First typed read-only II.6.4b adapters, explicit authorization/gallery scope, strict bounded DTOs, custom categories; not connected to production dispatch. |
| `.github/qa/managed-probe.py`, `.github/workflows/native.yml`, `meloader/packaging/pins.env` | Actual executable diagnostics, optional pinned Python and checksum-pinned standalone QA, targeted draft gates; full ready-PR verification still required. |

## Diagnostic transfer and offline contract

Start offline; independently read persisted consent before the trusted custodian creates a one-shot ephemeral loopback listener and dormant child. No caller-controlled destination. Controller waits for an incomplete HTTP response, requests offline, holds cleanup behind a bounded test barrier, verifies effective admission revoked and new start refused, then releases barrier. Stop must prove process cleanup and disconnect before server closure and drained acknowledgement. Library persists offline, local version remains available, shutdown verifies matching receipt. Explicit no-child refusal may release an empty lease; ambiguity cannot. Production needs the same obligations at all entry points, not the diagnostic markers or loopback test barrier. The gate is cooperative policy enforcement, not an OS sandbox; same-user fixture digest/marker files do not authenticate a release. Mac standalone asset not qualified.

## Inventory starting points for production adapter review

These are known navigation points, not a claim of exhaustive coverage. Review the current tree and build an explicit per-operation authorization/side-effect inventory before wiring any production dispatch. Use the north star's existing direction and policy table. Preserve external plugin approval and all current gallery/job/queue semantics.

| Area | Starting files or packages | Review obligation |
| --- | --- | --- |
| Legacy launch and UI/API | `cmd/monbooru/meloader.go`, `internal/api`, `internal/web`, loader `cmd/monloader` and HTTP routes | Preserve existing paths during migration; no silent legacy fallback from failed managed startup. Scope by explicit gallery/job; no active-gallery inference; inspect lifecycle and write locks. |
| Library callbacks | `meloader/internal/monbooru/client.go`, `managed_catalog.go`, `internal/api/managed_catalog.go` | Typed catalog, alias/category/digest/thumbnail and ingest permissions; no arbitrary file/database access. |
| Runtime execution | `meloader/internal/gdl/gdl.go`, `strict.go`, `executor.go`, `managed.go`, platform proc helpers and directlink implementation | Inventory version, extractor catalog/options, metadata resolve/first post, strict positive/negative predicate probe, strict transfer, streaming/catch-up, fallback, Python/venv/pip install/update/revert. Build only trusted typed command plans, no caller command/env/path. Preserve distinct parse/exit/cleanup results. Qualify release identity separately from execution. |
| Strict pipeline and source semantics | `meloader/internal/pipeline`, source/taxonomy packages, `meloader/docs/architecture/source-semantics-proposal.md` | Metadata and authoritative source identity/taxonomy before transfer; verified aliases, rating and provenance; unrated distinct from invalid; fail closed. User-editable blacklist; no automatic relaxation or AI filter. Existing strict integration uses legacy execution in its regression, not the new broker. |
| Queue/watch, PTR, lookup, settings | `meloader/internal/queue`, PTR and lookup packages, HTTP handlers, runtime installers and all HTTP client construction sites | Durable held work offline, explicit admission/drain for each actual WAN and child process path; local lookup must not implicitly go online; no background acquisition after quit. Redact secrets. Inventory ffmpeg/other subprocess paths and distinguish local vs WAN work. |
| Data commit | `internal/library`, `internal/api`, existing upload/replace/enrich and outcome handlers | Subsequent II.6.4c: destination-aware rooted staging outside scan trees, size/hash/provenance validation, no-clobber commit/verified-copy fallback, durable op ID and receipt reconciliation. No worker direct gallery DB writes or exactly-once claim across independent DBs. |

Keep version 1.32.19 qualified pending compatibility tests for newer release (II.6.5); Chapter IV bundles verified updates and rollback. Keep the actual runtime trust identity (including dependencies/interpreter) distinct from the fixture digest. Do not remove the diagnostic tests or conflate passed development gates with full milestone acceptance. Real queue, staging and runtime installation are not yet migrated. See the north star for sequencing, decisions and workflow.


### Custody pipe cancellation correction (candidate)

Windows development run 45968708640 timed out at TestCustodyUnconfirmedStopNeverPublishesReceipt, with Close waiting on a synchronous ReadFile while the writer was still held open. Readiness has the same pattern. Single-reader availability polling for the one-byte control channels now avoids speculative blocking reads and joins the liveness reader before closing. Windows PeekNamedPipe, Darwin/Linux poll, unsupported platform refuses. No change to exact cleanup receipt requirement or launch authorization. Hosted verification pending; this does not establish cancellation behavior for other framed IPC streams.
