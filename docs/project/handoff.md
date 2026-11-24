# Mebooru / Meloader — next-chat handoff

**Snapshot:** 2026-11-24. **Mebooru working branch:** `mebooru-foundation` at `2958fed7378801ebe207154efbc5cc285cffaae1` (PR #11 merge). **Meloader working branch:** `main`, observed at `1a0cef7a3b585a59eaa33592905d68e51e556bf8` during this refresh. Recheck live GitHub heads before editing; a snapshot is not a floating reference.

## Project goal and fixed boundaries

Mebooru is a lightweight local-first media gallery with independent gallery databases, a persistent gallery switcher, search and tags, relationships, duplicate handling, source provenance and optional local AI tagging. Meloader is the separately deployed, explicitly WAN-capable acquisition/reverse-lookup process. The Loader tab embeds Meloader's queue, Watches and PTR pages rather than making the Mebooru library core contact the internet. Preserve the privacy boundary, existing source rating and provenance trust, local-only media storage, and backward-compatible legacy `monbooru` / `monloader` Go packages, config keys, paths and cookies until a deliberate migration is justified.

Conceptual, product-behavior, trust, privacy and user-facing policy changes require discussion. Reliability refactors and in-scope documentation/testing may proceed without changing approved semantics. The operator can test on Apple Silicon macOS, not directly on Windows or Linux; CI covers the latter. Always identify what was observed live on Mac versus simulated or verified in CI.

## Latest completed work

- [Mebooru PR #9](https://github.com/jwebb7847-ui/mebooru/pull/9), merged as `2cc1df1`: selectable static video thumbnails or animated WebP-on-hover, persistent preference, FFmpeg discovery in macOS Homebrew paths and thumbnail rebuild guidance. Operator verified animated previews in the Mac test app. The static mode and error paths have automated coverage.
- [Mebooru PR #10](https://github.com/jwebb7847-ui/mebooru/pull/10), merged as `946cc21`: active-gallery Stats (image/video/archive counts, indexed size, catalog tags and tags in use), including missing-item handling and empty gallery test. Operator's disposable `Stats_QA` / `Stats_Empty` check passed.
- [Mebooru PR #11](https://github.com/jwebb7847-ui/mebooru/pull/11), merged as `2958fed`: independent rating-ceiling and unrated-visibility controls, a virtual Unrated entry in the Rating sidebar, and `total / shown / hidden` footer numbers. Does **not** create an unrated database tag or alter media/source metadata. Cross-platform CI: [five native jobs passed](https://github.com/jwebb7847-ui/mebooru/actions/runs/45323146804). Operator reviewed functioning UI on Mac.
- Finder-built `Mebooru.app` was successfully opened with bundled Meloader, and the operator completed live Derpibooru imports, after manual pairing. **Bundled companion process startup works; first-party automatic pairing is not yet implemented.**

See the [dated acceptance record](../validation/mac-acceptance-2026-11-24.md) for exact evidence and numbers, including why older locally synced files could display under SFW before PR #11.

## Most recent operator test state (historical, not a permanent fixture)

After intentionally resetting the prior database, the `default` gallery contained **101 present items**, reported as **95 images and 6 videos** in Stats. Rating audit: **50 explicit**, **49 general**, **2 unrated**; the catalog had **1,079 tags**. With Explicit + Unrated shown, the footer read **101 total / 101 shown / 0 hidden**, and the Rating sidebar listed all three groups. The two unrated items were independently verified with `tagcount:rating=0`. The earlier `MLP` gallery's 249 unrated + one explicit and the earlier `default` 95-general import are **separate, pre-reset audit snapshots**: do not assume those contents still exist.

For a *bare gallery* (empty search), expected counts if the 101-item fixture is unchanged:

| Ceiling | Unrated preference | Shown | Hidden |
| --- | --- | ---: | ---: |
| Explicit | shown | 101 | 0 |
| Explicit | hidden | 99 | 2 |
| SFW | shown | 51 | 50 |
| SFW | hidden | 49 | 52 |

The footer's `total` means inventory and `shown/hidden` refer to the current query and visibility. Stats inventory is unaffected by the filters. Filtering is a UI preference, **not** a security/access-control boundary. A real post's safe source rating maps to Mebooru `general`; explicit maps to `explicit`. Local disk synchronization does not infer ratings from depicted content or folder names.

## Latest known Mac environment and launch behavior

Apple Silicon MacBook Air; operator reported Go `1.27.4`, Homebrew `ffmpeg-full` with animated WebP support, and managed gallery-dl `1.32.19` after a Python environment repair. Mebooru serves loopback `127.0.0.1:8455`, Meloader `127.0.0.1:8456`. The test app used on the operator's Mac was `~/Applications/Mebooru-PR11-8edf75d.app` (PR head `8edf75d`, **not** the merged commit `2958fed`). GitHub merging does not change that installed executable. The default per-user state is historically under `~/Library/Application Support/monbooru` and `~/Library/Application Support/monloader`; always check current config and the Settings → Stats DB path rather than assuming.

Before changing anything, check which executable owns the listener: `lsof -nP -iTCP:8455 -iTCP:8456 -sTCP:LISTEN`. Don't start duplicate instances or delete/overwrite any gallery database. Build from the correct development branch and use a fresh checkout; see the [current Mac guide](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/local-audit.md). Never paste a real API token, private TOML or sensitive media into issue comments.

The Finder bundle starts its own Meloader companion, but a fresh profile still needs **Meloader Settings → Pairing → Connect** followed by **Mebooru Settings → Plugins → Approve**. The job Queue uses **To [gallery]** as the actual destination, which can differ from the gallery an operator intended. Creating folders under one gallery does *not* automatically create separate Mebooru galleries: galleries are configured explicitly.

For strict downloads, keep **Require blacklist verification before downloading** enabled. Its accepted content policy is to exclude violent/ill-willed content without categorically excluding consensual nonviolent adult material. A tag/alias-based blacklist is not a guarantee about every image's contents. A standalone post URL imports at most that post; a search URL supports multi-post jobs. The job reports creations, skips and blacklist outcomes separately.

## Prior Mac environment failure and repair

One strict queue job refused transfer with `mapping_failed: strict blacklist: gallery-dl could not validate the pre-download filter`, while Trace passed. Managed gallery-dl `1.32.19` had installed under Apple-supplied Python 3.9 linked against LibreSSL 2.8.3. `urllib3` emitted `NotOpenSSLWarning` on stderr: both synthetic filter probes exited 0, but the warning triggered the preflight refusal. The operator rebuilt the managed virtual environment with Homebrew Python 3.13/OpenSSL and then reported **one image created, 40 tags sent** for Derpibooru post `3932573`. Preserve strict verification; do not suggest disabling it as a workaround. Version `1.32.19` is the **validated baseline**, not a permanent feature requirement; later gallery-dl versions must be tested against the strict filter and source adapters.

## What to do next

Use the [prioritized backlog](backlog.md). Highest-value product work: **secure automatic bootstrap/pairing of the known bundled Meloader on first Finder launch**, without relaxing permissions for separately installed/remote plugins. Then address remaining mascot/empty-state brand polish and the cosmetic sidebar alignment. Include restart, fresh profile, port conflict, pairing revocation/recovery and privacy checks. Keep product changes in feature PRs, run native CI, and ask for Mac acceptance before merging.

## Pointers and verification

- [Mebooru project direction](../../MEBOORU.md), [Mebooru README](../../README.md), [backlog](backlog.md).
- [Meloader roadmap](https://github.com/jwebb7847-ui/meloader/blob/main/ROADMAP.md), [handoff](https://github.com/jwebb7847-ui/meloader/blob/main/docs/project/handoff.md), [source semantics](https://github.com/jwebb7847-ui/meloader/blob/main/docs/architecture/source-semantics-proposal.md), [local audit](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/local-audit.md), [strict blacklist](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/blacklist-timing.md).
- Existing backlog comments on [PR #11](https://github.com/jwebb7847-ui/mebooru/pull/11#issuecomment-7120527340) and [cosmetic follow-ups](https://github.com/jwebb7847-ui/mebooru/pull/11#issuecomment-7121920974).
- [Meloader performance evidence](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/negative-path-throughput.md), [rating integrity](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/rating-integrity.md).
