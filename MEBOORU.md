# Mebooru — project north star

**Updated:** 2026-11-25. **Active milestone:** II.4 — unify build and verification.
**Authority:** this file is the single maintained source of truth for product direction, architectural decisions, chapter roadmap, current milestone and outstanding work. Update it at each accepted milestone. Other documents are historical evidence, technical references or runbooks, not independent roadmaps. Start here on every new project chat; verify live GitHub refs before editing.

## Product vision and approved boundaries

Mebooru is a personal, local-first image/media viewing and organization application, evolved from Monbooru and Monloader. One repository, one installation, one UI, one settings experience and one application lifecycle. Efficiency matters, but minimizing process count or package size is not a goal in itself.

- Preserve independent galleries, database-first organization, managed/referenced media, original media quality, search, tags/aliases/relationships, duplicates, source provenance, rating controls, video previews and optional local AI. Preserve existing support for user-supplied media formats; do not create compressed archives as part of acquisition.
- Meloader becomes an internal managed acquisition worker, not a separately navigated application. No user-facing companion pairing, second settings page, manual port setup or separate launch is required in the finished product. Keep explicit approval for genuinely external plugins.
- Separate library data ownership from acquisition state ownership. The library owns media and gallery databases; the acquisition worker owns queues, watches, source caches and download bookkeeping. Transfer approved files and metadata through a narrow interface; do not write gallery databases directly from the worker.
- Hybrid lifecycle (Option C): start/wake the worker for acquisition requests and scheduled watches while Mebooru is open; stop when safely idle as appropriate. Explicit offline control blocks network-capable work, preserving local browsing and durable queued state. No requirement to operate after app close; no hidden agents or OS background services by default. A prominent Quit Mebooru action stops owned services gracefully.
- Preserve the offline library / WAN-capable acquisition boundary, strict metadata-first blacklist checks (fail closed), source rating and taxonomy evidence, verified alias matching, provenance and manual user overrides. Separate processes are not an OS-enforced sandbox; evaluate least-privilege controls during worker design. No bypass of source restrictions or rate limits.
- Bundle a self-contained gallery-dl runtime with verified managed updates and rollback; no ordinary user requirement for Homebrew or Python. Version 1.32.19 is the current strict transfer code's exact accepted version; validate any change before activation.
- Personal-use project: no separate licensing-analysis workstream. Retain existing license/attribution files and history during import. Distribution, signing and platform packaging remain separate future decisions.

## Chapter roadmap

Status vocabulary: **Complete** = verified accepted work; **Active** = current milestone, not yet complete; **Planned** = not implemented. Numbered milestones below are the authoritative sequence; later detailed substeps may be refined here before implementation. Discuss material product/trust/privacy changes with the owner; engineering implementation choices may be recommended and tested independently.

| Milestone | Status | Scope and exit criterion |
| --- | --- | --- |
| I — Establish the foundation | Complete | Library, acquisition, metadata, safety, integrated Loader, automatic pairing, detail regression and visual cleanup accepted. See evidence below. |
| II.1 — Repository reconnaissance | Complete (design review) | Inventory both exact trees, shared divergence, dependency versions, contracts, state and packaging. No new builds/tests run in this review. |
| II.2 — Freeze the baseline | Complete | Preserve exact pre-consolidation refs in both repos, verify rollback branches/tags, retain known-good app and establish data backup plan. Do not start import until rollback references are confirmed. |
| II.3 — Import Meloader | Complete | Non-squashed subtree or equivalent history-preserving merge into `meloader/` on a feature branch. Verify imported tree identity and original commits reachable. No functional source changes. |
| II.4 — Unify build and verification | Active | One checkout builds both existing executables. Retain both Go modules and dependency versions, root CI covers both modules and paired smoke, race/vet, strict transfer and security scanning. Test fresh and copied existing profiles. |
| II.5 — Reconcile shared infrastructure | Planned | Compare shared files individually; preserve stronger safeguards and tests. Decide common module/package layout only after II.4 parity. Avoid hidden dependency upgrades. |
| II.6 — Formalize worker interface | Planned | Define library/worker operations, permissions, compatibility, lifecycle, failure recovery and private communication. Current HTTP contract is transitional; exact IPC/sandbox design requires review. |
| III — Unify the experience | Planned | Native Mebooru Loader views and settings (remove embedded companion settings), internal service management, hybrid lifecycle, explicit offline mode, worker recovery, graceful Quit. No separate first-party plugin management for users. |
| IV — Self-contained acquisition | Planned | Bundled gallery-dl, verified updates, compatibility probes, safe activation and rollback on supported platforms. |
| V — Advanced acquisition | Planned | Deterministic recurring character/source queries, durable checkpoints, gallery selection, multi-source watches, pause/cancel/retry, ordinary direct-view downloads. |
| VI — Product refinement | Planned | Remaining usability and compatibility inventory, final security, performance, platform packaging and release acceptance as appropriate for personal use. |

**Next action:** complete II.4 existing-profile and bounded acquisition acceptance on a disposable baseline-created profile, then merge unified build PR #20. Before testing real data, complete a consistent data backup and isolate paths. Do not begin functional shared-package changes until II.4 acceptance. Do not combine import, module deduplication, UI redesign and storage changes in a single PR.

## Chapter II — migration baseline and evidence

Working branches: Mebooru `mebooru-foundation` (not upstream-derived `main`); Meloader `main`. Baseline snapshots frozen before the documentation update:

| Repository | Exact rollback commit | Verified rollback branch | Verified tag |
| --- | --- | --- | --- |
| Mebooru | `ef7d0ad0ab235c7d4662353e6fec87e47cd83866` | [rollback/pre-consolidation](https://github.com/jwebb7847-ui/mebooru/tree/rollback/pre-consolidation) | `baseline/pre-consolidation` (verified) |
| Meloader | `10542e66537430d21cc295449103bb2832e0693a` | [rollback/pre-consolidation](https://github.com/jwebb7847-ui/meloader/tree/rollback/pre-consolidation) | `baseline/pre-consolidation` (verified) |

Both rollback branches were created and read back at the exact SHAs on November 25. Branches/tags are not intrinsically immutable; do not move or delete these refs. The operator created both lightweight tags through authenticated Git on November 25; independent GitHub readback confirms both tags point to the exact commits above. Local mirror backups and self-contained Git bundles were created and verified by the operator: Mebooru 6,393 objects / 40 refs; Meloader 3,210 objects / 57 refs. Stored under the project backups/pre-consolidation.5M193N directory. Keep them private; do not mirror-push these backups. Operator copied the accepted app to the backup's accepted-app/Mebooru.app, verified its signature and compared both executables byte-for-byte. II.2 source and executable recovery complete. Before any real-profile test, make a consistent runtime data backup and isolate copied media paths. No runtime data has been copied or modified in this milestone. Keep both original repositories and the known-good app through migration; no requirement to retain redundant repos forever after acceptance. The verified local Git bundles provide independent backup. The app and its user data are not backed up by Git.

### Reconnaissance findings (II.1)

- 554 tracked blobs in Mebooru, 378 in Meloader; 128 overlapping paths, 72 identical and 56 different. Shared directory: 69 common paths, 64 identical, five divergent, one additional Meloader probe test. 17 and 26 Go test files respectively. Complete tree enumeration; selected implementation reads, not an exhaustive security audit.
- Shared divergences: desktop/probe.go (Meloader local-IP/no-redirect hardening plus probe tests); fsx/fsx.go and rename_other.go/rename_windows.go (Mebooru root-constrained durable writes and retry helpers); session/http.go (redirect-path validation). Preserve safeguards from both; regression-test rather than blind overwrite.
- Both Go 1.27.4 and TOML 1.6.0. Mebooru/Meloader SQLite 1.60.1/1.61.1, x/crypto 0.57.0/0.58.0, x/sys 0.48.0/0.49.0. Keep separate go.mod/go.sum initially; no go.work-driven dependency change by default. Shared contract and pairing code are identical.
- Existing library and worker HTTP APIs, loopback addresses, executable names, config keys, cookies, profile paths and state stores remain unchanged during import. Current Loader Queue is native; other panels/settings are embedded. Separate processes persist after future UI consolidation.
- Watches already support persistent URL, interval, item limits, history, retries and blacklist outcomes; current Watch record lacks its own destination gallery. gallery-dl already has managed installation/checksum machinery, but strict.go enforces 1.32.19 exactly. Existing managed revert removes managed runtime, not a transactional version rollback.

### II.3–II.4 implementation guardrails and acceptance

II.3 completed November 25: [PR #19](https://github.com/jwebb7847-ui/mebooru/pull/19) merged at `57277c942ace82cdade7a5e35e4ccc23c4efcb9c`. Exact original parents of import `dc3e419cabd69064cfac01cefc674048c837e37d` and identical nested tree `ad65075c852dcff551073e323aed93bfa22f862f` independently verified; merge tree matches import. Four hosted native jobs passed; Apple Silicon hosted queued at merge, not green. User approved merge after successful six-item fresh-profile Mac acceptance of II.4 candidate `225b690a5a6f0e47c7706679177b1b57dd1a52bc` with unchanged application source. [PR #20](https://github.com/jwebb7847-ui/mebooru/pull/20) remains pending existing-profile and bounded acquisition acceptance; its four native jobs, race/vet, pinned strict transfer regression and Go CodeQL passed at that revision. Hosted Apple Silicon still queued. Runtime data has not been tested against real profiles. II.4 follow-up preserves the previously observed Meloader CodeQL language coverage (Actions, JavaScript/TypeScript, C/C++ as well as Go); fresh acceptance remains evidence for the unchanged application source, while modified workflow checks must rerun.

Keep Mebooru at repo root and import Meloader as `meloader/` with its own module, assets and package paths. Do not overlay roots or squash away the imported history. Verify Git tree equality for imported source and reachability of both original commits; preserve rollback refs and accepted executable. Consolidate root CI explicitly: nested .github workflows do not execute automatically. Replace external companion checkout with nested path; adapt build scripts, cache keys, version stamps and root security scanning without lowering test coverage. Keep both binaries and current APIs; no user-data migration in the initial consolidation.

Acceptance: original history and source tree intact; both dependency graphs unchanged; clean checkout builds both executables; Go tests/race/vet and strict transfer tests; native five-platform matrix and CodeQL; fresh pairing, restart/revoke/refusal, copied existing gallery/queue/settings and image detail; bounded permitted acquisition with metadata; rollback build remains usable. Track hosted Apple Silicon cancellation separately from local Mac evidence. Never call cancelled/queued CI green.

For real-profile testing, stop both services; back up actual configured gallery paths, databases (including consistent SQLite state), config, queue.sqlite, download history, mapping/blacklist rules, taxonomy, source auth and pairing credentials securely. Test copied profiles first and inspect absolute paths to ensure they cannot reference real media. Do not delete or move user media or state to simulate a new installation.

## Advanced acquisition (Chapter V)

Ordinary downloading to directly viewable files in a chosen gallery is the preservation mechanism. No separate archival mode, compression/container format or agentic background service. Existing download "archive" refers to deduplication bookkeeping, not media compression. Reuse existing Watches where practical.

Proposed design to review, not yet committed UI or schema:
- Reuse existing Watches, queue and download-history mechanisms where possible; user selects sources, source-specific query or verified character mapping, destination gallery, and manual/scheduled run.
- Maintain a durable per-source checkpoint/seen-post history; handle pagination, retries, overlap, missed intervals, source changes, duplicate and alias policies without silently losing eligible posts. Historical matches use the ordinary download queue; recurring watches discover new eligible posts. Exact checkpoint and initial-run behavior remain design decisions.
- Apply the existing strict metadata-first blacklist and fail-closed transfer checks, preserve source ratings, taxonomy, aliases, provenance and user overrides. Never bypass source restrictions, rate limits or missing filter evidence.
- Show progress, results (created/skipped/blacklisted/failed), pause/cancel, and resume/retry. Offline control stops network work and retains durable state; no internet requests merely because the gallery is open in offline mode.

## Additional outstanding work (track here, not in separate backlogs)

- **Meta category:** source Meta labels are useful for search but overpopulate the sidebar. The November sample found 44 Derpibooru-backed Meta names (29 appearing on one file, five on two, ten on three or more) versus ten file-derived Meta names. User prefers **retaining** these tags. Manual workaround: drag Meta to the bottom in **Categories** (order persists per gallery). Discuss any future hide/collapse/low-use threshold before implementing; do not discard provenance.
- **Gallery semantics:** media subfolders inside one gallery are not auto-created galleries. This was clarified as intended behavior, not a defect. Revisit only if the user explicitly chooses a folder-to-gallery workflow.
- **Unrated classification/recovery:** PR #11 now *filters* unrated images; it does not infer ratings or backfill them. A future opt-in source hash lookup could recover authoritative ratings for local files. Never infer `explicit` or `general` from folder names or modify a user's manual rating silently.
- **gallery-dl upgrades:** `1.32.19` is a tested strict-blacklist baseline, not forever pinned for convenience. Validate newer releases through synthetic strict filter self-tests, Derpibooru/e621/Danbooru and real bounded imports before updating recommendations. Consider a clearer error when Python startup warnings break preflight; keep the fail-closed behavior and don't relax blacklists to mask warnings.
- **Packaging:** local Finder build is ad-hoc signed and Mac-tested. Public macOS distribution still needs signing/notarization; Windows and Linux installer packaging require actual platform acceptance beyond CI build/run verification. Decide packaging/distribution when functional requirements are met.

Remaining compatibility-critical historical names and optional legacy artwork are intentionally retained. Inventory during consolidation; do not rename persisted paths, protocol IDs, Go imports, env keys or cookies merely for appearance.

## Chapter I acceptance and operational references

- [PR #9](https://github.com/jwebb7847-ui/mebooru/pull/9): video preview modes; [#10](https://github.com/jwebb7847-ui/mebooru/pull/10): gallery Stats; [#11](https://github.com/jwebb7847-ui/mebooru/pull/11): rating/unrated controls.
- [Mebooru #13](https://github.com/jwebb7847-ui/mebooru/pull/13) / [Meloader #24](https://github.com/jwebb7847-ui/meloader/pull/24): automatic fresh-profile pairing, permission persistence/recovery, acquisition and image detail fix. [Pairing design](docs/project/bundled-pairing.md), [Mac acceptance](docs/validation/mac-acceptance-2026-11-24.md).
- [Mebooru #15](https://github.com/jwebb7847-ui/mebooru/pull/15) / [Meloader #25](https://github.com/jwebb7847-ui/meloader/pull/25): setup/sync logo, text-face removal and Unrated alignment; eleven manual checks passed; four hosted native platforms green, Apple Silicon hosted cancelled. [Visual acceptance](docs/validation/mac-visual-acceptance-2026-11-25.md). [#16](https://github.com/jwebb7847-ui/mebooru/pull/16) documentation closeout.
- Original upstream-derived audit checkpoint: `mebooru-audited-baseline-v1.24.0`. Retain current functional baseline above as the migration fallback instead of reverting to upstream.
- [Historical handoff / operating context](docs/project/handoff.md), [documentation index](docs/README.md), [Mac build and troubleshooting runbook](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/local-audit.md), [source semantics](https://github.com/jwebb7847-ui/meloader/blob/main/docs/architecture/source-semantics-proposal.md), [strict blacklist evidence](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/blacklist-timing.md). Historical Meloader roadmap/handoff are reference material only; active product decisions live here.

## Development workflow

All new Mac checkouts/builds/disposable test profiles belong under `$HOME/Downloads/Projects/Coding/Mebooru/testing` (unique candidate directories). Existing real runtime profiles and media stay in place. Operator can test Apple Silicon Mac; Windows/Linux coverage is automated unless separately accepted. Keep exact test SHAs, PRs, and distinctions between manual and hosted checks. Use feature branches, scoped PRs and explicit acceptance before functional merges. No private tokens, credentials, actual media or private config in GitHub. Update this file at milestone closeout; keep technical specifications and dated evidence only when independently useful. Do not create another parallel roadmap.
