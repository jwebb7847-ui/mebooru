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

- `mebooru-foundation` is the fork's working branch. Its last code checkpoint, [`fbb5232`](https://github.com/jwebb7847-ui/mebooru/commit/fbb523264c233a4a163a6627432221c0f5c02f4d), includes the persistent gallery switcher, gallery banners and the native gallery image picker.
- `main` remains at the upstream-derived v1.24.0 baseline. The [audited baseline tag](https://github.com/jwebb7847-ui/mebooru/tree/mebooru-audited-baseline-v1.24.0) identifies the original validation checkpoint.
- The [cross-repository handoff](https://github.com/jwebb7847-ui/meloader/blob/main/docs/project/handoff.md) and [local audit runbook](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/local-audit.md) record the current paired workflow. The Mac audit uses this development branch with an explicit config, loopback port 8455 and the disposable `Audit_A` gallery.

## Acquisition boundary

Meloader is the optional, explicitly WAN-capable acquisition companion. **Loader** opens its Queue, Watches, and PTR screens inside Mebooru. **Settings → Meloader** opens the companion settings. The screens use Meloader's configured browser origin and its own login. Pairing and the browser-facing Web URL are configured in **Settings → Plugins**. Use distinct origins for the two services. The Mebooru server's paired API client remains separate from the browser panels.

The Loader queue shows the active gallery as the destination for new imports. Each gallery has its own media folder, database, and thumbnail cache. The queue is shared across galleries. Watches use Meloader's existing source and default gallery settings, with a manual default when created in Loader. [Paired audit instructions](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/loader-integration.md)

## Compatibility policy

For now, internal Monbooru identities such as the Go module path, package imports,
configuration keys, environment variables, cookies, API protocol names, and
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
