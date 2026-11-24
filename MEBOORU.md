# Mebooru

Mebooru is a personal fork of Monbooru, currently based on Monbooru v1.24.0
(commit 0ac84b9).

The upstream Monbooru project remains the architectural foundation and is
credited through the existing repository history, license, contributor records,
and upstream documentation.

## Project direction

Mebooru is intended to remain a lightweight, local-first personal media library
with:

- independent galleries/libraries
- database-first organization
- source metadata and provenance
- managed and referenced media workflows
- first-class duplicate handling
- source-driven tag acquisition and normalization
- optional local AI tag enrichment
- fast gallery switching and discovery
- explicit privacy boundaries for network-enabled features
- future server use without clients directly sharing the SQLite database

## Development baseline

- `mebooru-foundation` is the fork's working branch. Its latest accepted November 24 milestone is [PR #13](https://github.com/jwebb7847-ui/mebooru/pull/13) merged at `c5319ee` with [Meloader PR #24](https://github.com/jwebb7847-ui/meloader/pull/24) at `b5b9ddb`; it adds first-run automatic pairing and corrects image detail rendering. Earlier work in PR #11 and preceding PRs includes gallery switching and banners, integrated Loader, [video WebP/static previews](https://github.com/jwebb7847-ui/mebooru/pull/9), [Stats](https://github.com/jwebb7847-ui/mebooru/pull/10), and independent unrated/rating visibility controls.
- `main` remains at the upstream-derived v1.24.0 baseline. The [audited baseline tag](https://github.com/jwebb7847-ui/mebooru/tree/mebooru-audited-baseline-v1.24.0) identifies the original validation checkpoint.
- The [cross-repository handoff](https://github.com/jwebb7847-ui/meloader/blob/main/docs/project/handoff.md) and [local audit runbook](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/local-audit.md) record the current paired workflow. The historical source-based audit used an explicit config and disposable `Audit_A` gallery; the later Finder acceptance and current status are documented in [the November 24 record](docs/validation/mac-acceptance-2026-11-24.md).

## Acquisition boundary — current implementation

Meloader is the optional, explicitly WAN-capable acquisition companion. **Loader** opens its Queue, Watches, and PTR screens inside Mebooru. **Settings → Meloader** opens the companion settings. The screens use Meloader's configured browser origin and its own login. Pairing is configured in **Settings → Plugins**. Direct local HTTP uses the paired loopback address when the browser-facing Web URL is blank. Use distinct origins for the two services. The Mebooru server's paired API client remains separate from the browser panels.

The Loader queue shows the active gallery as the destination for new imports. Each gallery has its own media folder, database, and thumbnail cache. The queue is shared across galleries. Watches use Meloader's existing source and default gallery settings, with a manual default when created in Loader. [Paired audit instructions](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/loader-integration.md)

## Compatibility policy

For now, internal Monbooru identities such as the Go module path, package imports,
configuration keys, legacy environment variable aliases, cookies, API protocol names, and
companion integration names remain unchanged unless there is a concrete reason
to migrate them.

This keeps the fork close to upstream and reduces unnecessary merge conflicts.

## Audited baseline

The v1.24.0 baseline was tested on Apple Silicon macOS before Mebooru development
began. The audit covered:

- native build and execution
- loopback-only default networking
- absence of observed telemetry or unsolicited WAN traffic
- multiple independent galleries
- filesystem watching
- original-file integrity
- per-gallery databases and thumbnail caches
- tagging and search
- duplicate handling
- Monloader pairing
- source-tag import
- source provenance
- metadata-first pre-download blacklist rejection

The audited baseline is tagged:

`mebooru-audited-baseline-v1.24.0`

## Current handoff and deferred work (2026-11-24)

Start with the [project handoff](docs/project/handoff.md), [prioritized backlog](docs/project/backlog.md), [Mac acceptance evidence](docs/validation/mac-acceptance-2026-11-24.md), and [documentation index](docs/README.md). These supersede the November 15 snapshot for completed Finder acceptance while preserving the older test history.

The packaged Finder Mac application automatically pairs its trusted bundled Meloader on a fresh eligible profile; existing, revoked and paused profiles retain their decisions. The next approved direction is [one product, one UI, one repository, separate managed acquisition worker](docs/project/unified-product-direction.md). This is an architectural decision, not an implemented migration. Legacy setup/status mascots, empty-result emoticons and virtual Unrated count alignment remain deferred. Do not imply that all former `mon` symbols can be renamed safely: some are compatibility-critical on-disk/config/protocol identifiers.
