# Mebooru unified product — architecture decision (2026-11-24)

**Status:** owner approved direction in discussion; no migration or refactor implemented. Baseline: Mebooru PR #13 merge `c5319ee504e32eef0f52e20969c705accbfc4e3c`, Meloader PR #24 merge `b5b9ddb103a50b396fd81af9b3d94e0c6f29bc46`. The current paired bundle remains the working fallback.

## Approved product direction

- One product, one installation, one Mebooru UI and settings system. Retain a separately managed network-capable acquisition worker rather than combining all work into one monolithic executable. Meloader becomes an implementation component, not a second application users must navigate or pair.
- Consolidate the repositories into Mebooru, preserving upstream attribution, license obligations, revision history and recoverable legacy repositories. Plan the import and dependency/Go-module layout before deciding exact structure. Do not delete the Meloader repository or replace the working branch prematurely.
- **Hybrid lifecycle (Option C):** the app manages worker startup for requests and scheduled watches, may stop it when idle, and provides a master offline control that prevents acquisition activity. No requirement to perform watches while the app is closed; do not add hidden background agents or OS services by default.
- Remove the cumbersome embedded Meloader settings mini-page. Integrate acquisition, accounts, blacklist, queue, watches, runtime diagnostics and updates into the main application settings/navigation. Core acquisition should be an enable/disable feature, not a removable external plugin. Third-party plugin approval stays explicit.
- Bundle a self-contained gallery-dl runtime with managed updates. No required manual Homebrew/Python install for ordinary users. Exact runtime packaging, licensing, verification, version compatibility, update transport and rollback remain design decisions.
- Provide a prominent Quit Mebooru action; graceful application shutdown stops owned workers and preserves queue state.

## Advanced recurring acquisition

The owner wants advanced users to configure selected boorus and collect newly available images related to a selected character (or analogous source query), excluding blacklisted items, with source tags retained. Ordinary downloading of directly viewable media to the chosen local gallery is the preservation mechanism. Do not introduce a separate archival mode, compression/container format, or a separate collection of inaccessible files for this purpose. Existing ordinary download deduplication history (sometimes called a download archive) is bookkeeping, not media compression. This is a deterministic rule-based workflow, not an agent. It should be usable while Mebooru is open and need not run after the app is closed.

Proposed design to review, not yet committed UI or schema:
- Reuse existing Watches, queue and download-history mechanisms where possible; user selects sources, source-specific query or verified character mapping, destination gallery, and manual/scheduled run.
- Maintain a durable per-source checkpoint/seen-post history; handle pagination, retries, overlap, missed intervals, source changes, duplicate and alias policies without silently losing eligible posts. Historical matches use the ordinary download queue; recurring watches discover new eligible posts. Exact checkpoint and initial-run behavior remain design decisions.
- Apply the existing strict metadata-first blacklist and fail-closed transfer checks, preserve source ratings, taxonomy, aliases, provenance and user overrides. Never bypass source restrictions, rate limits or missing filter evidence.
- Show progress, results (created/skipped/blacklisted/failed), pause/cancel, and resume/retry. Offline control stops network work and retains durable state; no internet requests merely because the gallery is open in offline mode.

## Guardrails and proposed migration sequence

1. Inventory both codebases, shared packages, licenses, upstream history, dependency/build constraints, API and current data formats. Design one-repo migration with tests and reproducible rollback; preserve separate process outputs. No data reset or forced path rename.
2. Establish unified build/test pipeline and explicit library/worker interface. Existing paired APIs may serve as a transition bridge. Private IPC and platform-specific least-privilege isolation are candidates, not finalized. Separate processes alone do not constitute OS-enforced sandboxing. Library core should have no external network requirement; restrict worker access to necessary inputs/outputs. Preserve offline library and external plugin behavior.
3. Consolidate UI/settings and implement worker lifecycle, explicit offline behavior, graceful Quit, and recoverable worker failures. Do not retain two independent settings experiences merely for compatibility; migrate old values with validation and user data protection.
4. Integrate managed gallery-dl runtime, verify platform and dependency compatibility; keep strict blacklist regression and version rollback gates. Extend existing watch workflows only after baseline behavior is protected.
5. Validate full feature parity, migration, backup/rollback, offline operation, worker crash/restart, watch checkpoints, blacklist and source metadata, and platform packaging. Keep cosmetic backlog and release signing/notarization visible.

Discuss unresolved product/trust decisions with the owner before implementing them. No public release or Windows/Linux hands-on validation is implied by prior build/run CI. No source files, executable packaging, or runtime state are changed by this decision record.
