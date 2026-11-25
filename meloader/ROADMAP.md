# Meloader / Mebooru project roadmap

> **Historical snapshot — superseded.** The active roadmap and milestone-level merge/acceptance workflow are maintained exclusively in [MEBOORU.md](../MEBOORU.md). Preserve the November 24 plan below as earlier evidence; do not implement its old branch or PR instructions.
> Last reviewed 2026-11-24. [Project handoff](docs/project/handoff.md) · [Mebooru handoff](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/project/handoff.md) · [Mac audit runbook](docs/validation/local-audit.md)

## North star

Meloader retrieves images **with the correct meaning of their source metadata**, using gallery-dl for broad source compatibility and small optional source-specific adapters when more precision is required. Mebooru maintains a coherent local library with trustworthy source provenance, browsing, search, tags, and relationships.

Keep the network boundary intact: Meloader is the explicitly WAN-capable acquisition companion; the Mebooru library core does not gain new outbound dependencies. They can eventually feel like one application through an integrated acquisition interface while remaining separate services at first.

**Important distinction:** Recovering *source taxonomy* (e.g. whether a tag is a character) is not the same as reconciling *cross-source vocabulary* (e.g. synonyms such as `flight` and `flying`). The latter is a separate library-level problem. Do not silently conflate the two.

## Architecture invariants

1. One generic gallery-dl / queue / download / upload pipeline. Do not reimplement it separately in each adapter.
2. Source profiles remain the broad compatibility fallback for standard acquisition. The opt-in strict blacklist has its own supported routes.
3. Adapters explain source semantics; generic rules, blacklist policy, destination selection, file handling, deduplication, and Mebooru communication stay outside adapters.
4. A source with trustworthy, fully categorized metadata must not incur network enrichment merely because an adapter is registered.
5. Preserve original source identity, attribution, and Mebooru tag/source provenance. Do not turn known-invalid metadata into silently accepted general tags.
6. Keep user overrides meaningful; discuss precedence and error/fallback behavior before changing their semantics.
7. Prefer bounded concurrency, batched requests, shared persistent caching, and explicit failure paths to unbounded per-post work.
8. Discuss changes to behavior, trust, privacy, or user-facing concepts before implementation. Reliability, testing, and nonbehavioral internal refactors may proceed independently.
9. Require tests and meaningful bulk-workload measurements before claiming a generalized implementation preserves speed.

## Current work and branches

- **Meloader `main`** contains Loader integration, queue restart recovery, branding and setup/PTR hardening; observed at `1a0cef7` on 2026-11-24. The earlier tested Mac companion checkout `8ac2d70` remains a historical pinned baseline, not the current head.
- **Mebooru `mebooru-foundation`** contains the integrated Loader, Mac launcher, video hover/static preview preference, Stats and unrated visibility; observed at [PR #11 merge `2958fed`](https://github.com/jwebb7847-ui/mebooru/pull/11). Its `main` still holds the upstream-derived v1.24.0 baseline. See [Mebooru handoff](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/project/handoff.md).
- **Latest acceptance.** Finder Mac launch, paired live imports, video WebP previews, per-gallery Stats and PR #11 unrated visibility passed operator checks. Five native Mebooru CI jobs passed on [PR #11](https://github.com/jwebb7847-ui/mebooru/actions/runs/45323146804). Bundled first-run auto-pairing merged in Meloader #24 / Mebooru #13; local Mac acceptance passed, with the hosted Apple Silicon runner exception recorded in the dated acceptance record. The next product direction is a unified UI and separately managed acquisition worker in one repository. See the Mebooru architecture decision. See [dated evidence](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/validation/mac-acceptance-2026-11-24.md).

## Milestones

### 1. Derpibooru taxonomy foundation — merged

Recover authoritative categories when gallery-dl emits flattened tags. Use the shared registry in `internal/taxonomy`, a persistent category cache, and the generic mapper. Revisit long-tail taxonomy semantics in Item 4.

### 2. Validate source taxonomy and optimization — complete

- A 20-post Derpibooru taxonomy workload took **22.99 s cold**, with 17 HTTP attempts including two rate-limit responses. Three warm reloads reproduced the same 442 mapped tag assignments, made **zero taxonomy HTTP attempts**, and took **1.38 ms median** for enrichment and mapping. This measures resolver and mapper work. [Cold/warm audit](docs/validation/negative-path-throughput.md#6-live-derpibooru-taxonomy-cache-audit)
- The bounded Safebooru continuation created 20 images in about **35 s**. The prior window created 15 and skipped five. [Live acquisition results](docs/validation/negative-path-throughput.md#bounded-20-post-safebooru-run-and-continuation--passed)
- After merging, a copied-state Derpibooru audit created five images, preserved ratings, categories and source links, restored queue history after restart, reused the taxonomy cache, and skipped all five on replay through the download archive. [Upgrade smoke test](docs/validation/negative-path-throughput.md#7-post-merge-derpibooru-upgrade-smoke-test)
- Bounded 200-post Derpibooru end-to-end tests ran during Item 5. A 400-image loopback run through both real apps completed the bulk acceptance check across two 200-post windows. It verified 400 imports, 400 media requests, zero failures and zero duplicate rows, and recorded first import, throughput, process memory and goroutines. The 400-image run replaced the planned 1,000-image run. Keep the 10,000-image run deferred until a concrete scale question calls for it. [400-image bulk acceptance](docs/validation/negative-path-throughput.md#9-local-400-image-end-to-end-benchmark)

### 3. Generalized source adapters — complete

- Optional enrichment and warming preserve generic profile support. Keep the registry in `internal/taxonomy` until a broader contract requires a move.
- Approved rating and taxonomy semantics preserve source authority and valid user filing overrides. Invalid required metadata fails visibly. Relevant download, refresh, lookup, replacement and bundle paths enforce the policy. Known-source ratings use the shipped family rules; a blank Philomena rating field can use the source rating tag. Bundles check each page after enrichment and before archive creation. [Approved contract](docs/architecture/source-semantics-proposal.md#product-decisions) · [Rating and malformed-input tests](docs/validation/rating-integrity.md)
- e621 categorized extraction, preview and one live import passed with the generic mapper. Retain **`lore → meta` and `contributor → artist`**. [e621 validation and decision](docs/validation/e621.md)
- Danbooru automated coverage and authenticated live acceptance passed. Post `294929` mapped `s` to `rating:sensitive` and imported into `Audit_A` with categorized tags and original attribution. [Live Danbooru result](docs/validation/negative-path-throughput.md#8-live-danbooru-acceptance)

### 4. Further booru coverage — complete

[PR #6](https://github.com/jwebb7847-ui/meloader/pull/6) shipped the approved trust and fallback fixes for malformed taxonomy, tag ID verification, and canonical post provenance. The Mac audit confirmed a live import and cache reuse after restart. The [opt-in pre-download blacklist](docs/validation/blacklist-timing.md) in [PR #7](https://github.com/jwebb7847-ui/meloader/pull/7) passed loopback media-request checks and a live Mac audit in `Audit_A`. [PR #8](https://github.com/jwebb7847-ui/meloader/pull/8) merged the [Mac-tested debug-to-Trace URL prefill](docs/validation/blacklist-timing.md#debug-to-trace-prefill) at `b0a0213`.

The filing policy for named original characters and unusual namespaces passed Mac Trace acceptance in [PR #10](https://github.com/jwebb7847-ui/meloader/pull/10). Verified Derpibooru aliases in [PR #11](https://github.com/jwebb7847-ui/meloader/pull/11) match blacklist rules in both directions and file under the canonical tag. The alias change passed automated media-request checks and live Mac Trace acceptance. [Filing and alias evidence](docs/validation/derpibooru-filing.md)

### 5. Meloader / Mebooru user integration — acceptance passed

The native Mebooru **Loader** tab exposes Meloader's Queue, Watches, and PTR controls, with companion settings under **Settings → Meloader**. The browser surfaces use the separate Meloader origin and retain the existing pairing and library privacy boundary. The bounded Mac audits covered a 200-post Derpibooru import, gallery switching and result links, strict verification timing, and explicit archive replay. The 20-post pacing comparison closed with the 1.0 s default retained. [Integration and validation](docs/validation/loader-integration.md)

### 6. Reliability and observability — acceptance passed

Improve acquisition diagnostics, retry visibility, structured timing, failure distinctions and regression monitoring without sacrificing snappiness.

- Job-scoped strict Derpibooru preflight diagnostics, distinct taxonomy failure labels, and the pinned gallery-dl regression are merged. [Diagnostic fields and loopback regression](docs/validation/loader-integration.md#strict-download-timing)
- The SQLite restart regression verifies completed and queued work, explicit retry of an interrupted window, and the next continuation offset.
- The paired-app interruption check recovered all missing allowed images, preserved existing library rows, and made zero media requests for blacklisted posts. [Evidence](docs/validation/loader-integration.md#paired-app-interruption-and-retry) · [Recovery steps](docs/validation/local-audit.md#recover-an-interrupted-import)
- The 400-image loopback run remains the baseline for changes that affect bulk processing. [Bulk acceptance](docs/validation/negative-path-throughput.md#9-local-400-image-end-to-end-benchmark)

### 7. Broader Mebooru library experience — integrated workflow accepted

The integrated queue shows import results and opens the destination gallery. The Mac audit reported Job #38 succeeded with 155 created and 45 blacklisted from a 200-post window after service connectivity was restored. Gallery restore and banner handling were hardened before packaging. Cross-source vocabulary consistency remains separate feature work. [Mebooru hardening PR](https://github.com/jwebb7847-ui/mebooru/pull/6)

### 8. Naming and packaging — personal Mac Finder candidate accepted

The paired app candidate passed native automated Linux x86-64/ARM64, Windows x86-64 and both macOS architectures, followed by **operator-confirmed Finder launch and Derpibooru imports on Apple Silicon macOS**. Historically, this candidate still required manual Connect/Approve; #24 and Mebooru #13 subsequently added fresh-profile automatic pairing. Retain legacy package/config/protocol identifiers for existing installs. Further branding, secure bundled auto-pairing, actual Windows/Linux installer acceptance, and public macOS signing/notarization remain. [Meloader branding PR](https://github.com/jwebb7847-ui/meloader/pull/19) · [Mac app PR](https://github.com/jwebb7847-ui/mebooru/pull/7) · [Current Mac acceptance](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/validation/mac-acceptance-2026-11-24.md).

### 9. Mebooru interface hardening — PRs #9–#11 merged

- [Mebooru PR #9](https://github.com/jwebb7847-ui/mebooru/pull/9) added a persistent Static/Animated WebP video preview preference, FFmpeg discovery and rebuild guidance; operator confirmed hover animations on Mac.
- [Mebooru PR #10](https://github.com/jwebb7847-ui/mebooru/pull/10) added per-gallery Stats and passed a disposable populated/empty gallery Mac comparison.
- [Mebooru PR #11](https://github.com/jwebb7847-ui/mebooru/pull/11) added independent rating ceiling and unrated visibility, a virtual Unrated sidebar filter, and total/shown/hidden counts. The 101-item operator fixture contained 50 Explicit, 49 General, 2 Unrated (verified via `tagcount:rating=0`); 95 images + six videos. [Five native CI jobs passed](https://github.com/jwebb7847-ui/mebooru/actions/runs/45323146804). The operator accepted behavior, and the PR merged at `2958fed`.
- A historical Meta audit found **44 source-backed** and **ten file-derived** Meta names. Source Meta should stay searchable. Manual category ordering can put Meta last. Local disk scans do not assign a sexual-content rating automatically.
- Mac strict download failure arose from `urllib3`/Python-LibreSSL startup stderr during a filter self-test, not source-rating mapping; it was resolved by rebuilding managed gallery-dl with Homebrew Python 3.13/OpenSSL. Keep verification on; validate newer gallery-dl versions before upgrading.

### 10. Next iteration — secure first-run integration and polish (not yet implemented)

1. Auto-initialize and **securely auto-pair only the trusted bundled, locally launched Meloader**, preserving manual approval for untrusted external/remote plugins and existing revoke/pause controls.
2. Visual cleanup candidate: remove default legacy mascot display and decorative queue/404 text faces; preserve custom themes and functional status labels. Mebooru handles setup/sync branding and Unrated alignment. Mac visual acceptance pending.
3. Consider optional source-backed rating recovery, higher-level Meta display controls and validated gallery-dl upgrade only after product discussion.
4. Perform release packaging acceptance separately (Mac signing/notarization; real Windows/Linux installers). Native CI does not establish those UX claims.

[Prioritized backlog](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/project/backlog.md) · [Handoff](docs/project/handoff.md)

## Local VS Code testing protocol

Follow the [local audit runbook](docs/validation/local-audit.md) for checkout, config, launch, restart, and browser instructions. Historical evidence belongs in the linked validation notes.

## Merge/acceptance gates

- Run `go test -race ./...` and `go vet ./...` on supported Go version. Live integration tests are separately opt-in.
- Verify unknown-source fallback, source provenance, ratings, user overrides, blacklist behavior and consistent errors.
- Ensure source metadata is never silently flattened or reclassified due to failure of a specialized adapter.
- Compare the existing source-adapter baseline and the candidate under representative warm-cache workloads; reject a meaningful user-perceived speed regression.
- For cold-cache workloads, measure requests, waiting, error handling, throughput and memory before making promises.
- Keep the proposed behavior-changing semantics in review until discussed with the product owner.

## Known open decisions

The [approved architecture defaults](docs/architecture/source-semantics-proposal.md#product-decisions) cover source field outcomes, provenance retention, taxonomy and vocabulary ownership, and the adapter package boundary.

- Define richer diagnostic fields and retention when a concrete audit or replay use case requires them.
