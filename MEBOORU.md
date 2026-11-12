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
