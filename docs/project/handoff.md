# Mebooru / Meloader — historical handoff and operating context

**Historical snapshot, not a live roadmap.** Begin with [MEBOORU.md](../../MEBOORU.md) for current decisions, chapter status and next action. Preserve this document for operational context and earlier acceptance evidence. Current branch/PR/CI instructions are in [Development workflow](../../MEBOORU.md#development-workflow); the old per-change PR process and separate repository instructions are superseded.

**Current resume pointer (2026-11-28):** II.6.4a accepted; b isolated development verified; c active. v3 native media/naming 49 modes and all seven Linux/Windows development jobs qualified. v4 collection on draft PR #38 at 22a9fa6, full gate running; existing-target read-only observer (20 proposed separate modes) prepared, auxiliary #43 running. Read [rolling resume checkpoint](../../MEBOORU.md#rolling-handoff--resume-checkpoint-2026-11-28), [guarded acquisition evidence](../validation/managed-guarded-acquisition-2026-11-28.md), [staging exit-gap review](managed-staging-exit-review.md). Historical snapshot below is operational context, not current branch status.

**Snapshot:** 2026-11-25, after visual closeout. **Mebooru working branch:** `mebooru-foundation`, feature merge `3715067d067417dabeec94c05f1b6bb90148f293` (PR #15). **Meloader working branch:** `main`, feature merge `10542e66537430d21cc295449103bb2832e0693a` (PR #25). Recheck live GitHub heads before editing; a snapshot is not a floating reference.

## Project goal and fixed boundaries

Mebooru is a lightweight local-first media gallery with independent gallery databases, a persistent gallery switcher, search and tags, relationships, duplicate handling, source provenance and optional local AI tagging. In the current implementation, Meloader is the separately deployed, explicitly WAN-capable acquisition/reverse-lookup process. The approved future architecture is a managed acquisition worker in a unified Mebooru product and repository; see [project north star](../../MEBOORU.md). The Loader tab embeds Meloader's queue, Watches and PTR pages rather than making the Mebooru library core contact the internet. Preserve the privacy boundary, existing source rating and provenance trust, local-only media storage, and backward-compatible legacy `monbooru` / `monloader` Go packages, config keys, paths and cookies until a deliberate migration is justified.

Conceptual, product-behavior, trust, privacy and user-facing policy changes require discussion. Reliability refactors and in-scope documentation/testing may proceed without changing approved semantics. The operator can test on Apple Silicon macOS, not directly on Windows or Linux; CI covers the latter. Always identify what was observed live on Mac versus simulated or verified in CI.

## Latest completed work

- [Mebooru PR #9](https://github.com/jwebb7847-ui/mebooru/pull/9), merged as `2cc1df1`: selectable static video thumbnails or animated WebP-on-hover, persistent preference, FFmpeg discovery in macOS Homebrew paths and thumbnail rebuild guidance. Operator verified animated previews in the Mac test app. The static mode and error paths have automated coverage.
- [Mebooru PR #10](https://github.com/jwebb7847-ui/mebooru/pull/10), merged as `946cc21`: active-gallery Stats (image/video/archive counts, indexed size, catalog tags and tags in use), including missing-item handling and empty gallery test. Operator's disposable `Stats_QA` / `Stats_Empty` check passed.
- [Mebooru PR #11](https://github.com/jwebb7847-ui/mebooru/pull/11), merged as `2958fed`: independent rating-ceiling and unrated-visibility controls, a virtual Unrated entry in the Rating sidebar, and `total / shown / hidden` footer numbers. Does **not** create an unrated database tag or alter media/source metadata. Cross-platform CI: [five native jobs passed](https://github.com/jwebb7847-ui/mebooru/actions/runs/45323146804). Operator reviewed functioning UI on Mac.
- [Mebooru PR #13](https://github.com/jwebb7847-ui/mebooru/pull/13) and [Meloader PR #24](https://github.com/jwebb7847-ui/meloader/pull/24) merged. First-run bundled auto-pairing, restart, pause/resume, revoke/restart, manual recovery, existing-profile upgrade and live acquisition passed on the operator's Mac; the image-detail regression was corrected and display confirmed. Exact accepted heads `1254471b5a7e149813af0a66a2a8ba212a018a6e` and `3913424a9ae66a5dc539bfd96c48fdc8fdf5e1e9`. Linux x64/arm64, Windows x64 and Intel Mac CI passed; final hosted Apple Silicon job queued after allocation failures at merge, explicitly accepted by owner based on local testing. [Acceptance evidence](../validation/mac-acceptance-2026-11-24.md).

- [Mebooru PR #15](https://github.com/jwebb7847-ui/mebooru/pull/15) and [Meloader PR #25](https://github.com/jwebb7847-ui/meloader/pull/25) merged; all eleven local visual/upgrade checks passed. Four native hosted platforms and Meloader Go/race/vet/CodeQL passed; hosted Apple Silicon job cancelled before recorded steps. [Visual acceptance](../validation/mac-visual-acceptance-2026-11-25.md).

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

Apple Silicon MacBook Air; operator reported Go `1.27.4`, Homebrew `ffmpeg-full` with animated WebP support, and managed gallery-dl `1.32.19` after a Python environment repair. Mebooru serves loopback `127.0.0.1:8455`, Meloader `127.0.0.1:8456`. The latest operator-tested app is the PR #15/#25 visual candidate under `$HOME/Downloads/Projects/Coding/Mebooru/testing/pr15-visual-cleanup*`; the exact generated folder suffix was not reported. Accepted heads are recorded in the visual acceptance document. The previous paired build is `testing/pr13-detail-fix/Mebooru.app`. The older candidate and isolated `profile-first-run` are under `testing/pr13-auto-pairing`. All new checkouts, builds and disposable test fixtures belong under `$HOME/Downloads/Projects/Coding/Mebooru`; keep real application data in its existing locations. GitHub merging does not change that installed executable. The default per-user state is historically under `~/Library/Application Support/monbooru` and `~/Library/Application Support/monloader`; always check current config and the Settings → Stats DB path rather than assuming.

Before changing anything, check which executable owns the listener: `lsof -nP -iTCP:8455 -iTCP:8456 -sTCP:LISTEN`. Don't start duplicate instances or delete/overwrite any gallery database. Build from the correct development branch and use a fresh checkout; see the [current Mac guide](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/local-audit.md). Never paste a real API token, private TOML or sensitive media into issue comments.

The Finder bundle starts its own Meloader companion and automatically pairs only eligible fresh standard profiles. Explicit manual recovery remains available via **Meloader Settings → Pairing → Connect** followed by **Mebooru Settings → Plugins → Approve**; the current unpaired shell lacks a convenient direct recovery route. Closing the browser tab does not stop the services; a discoverable Quit control remains planned. The job Queue uses **To [gallery]** as the actual destination, which can differ from the gallery an operator intended. Creating folders under one gallery does *not* automatically create separate Mebooru galleries: galleries are configured explicitly.

For strict downloads, keep **Require blacklist verification before downloading** enabled. Its accepted content policy is to exclude violent/ill-willed content without categorically excluding consensual nonviolent adult material. A tag/alias-based blacklist is not a guarantee about every image's contents. A standalone post URL imports at most that post; a search URL supports multi-post jobs. The job reports creations, skips and blacklist outcomes separately.

## Prior Mac environment failure and repair

One strict queue job refused transfer with `mapping_failed: strict blacklist: gallery-dl could not validate the pre-download filter`, while Trace passed. Managed gallery-dl `1.32.19` had installed under Apple-supplied Python 3.9 linked against LibreSSL 2.8.3. `urllib3` emitted `NotOpenSSLWarning` on stderr: both synthetic filter probes exited 0, but the warning triggered the preflight refusal. The operator rebuilt the managed virtual environment with Homebrew Python 3.13/OpenSSL and then reported **one image created, 40 tags sent** for Derpibooru post `3932573`. Preserve strict verification; do not suggest disabling it as a workaround. Version `1.32.19` is the **validated baseline**, not a permanent feature requirement; later gallery-dl versions must be tested against the strict filter and source adapters.

## Current planning

See [MEBOORU.md](../../MEBOORU.md). The snapshot above is historical; do not treat its branch SHAs or fixture counts as live.

## Pointers and verification

- [Mebooru project direction](../../MEBOORU.md), [Mebooru README](../../README.md), [backlog](backlog.md).
- [Meloader roadmap](https://github.com/jwebb7847-ui/meloader/blob/main/ROADMAP.md), [handoff](https://github.com/jwebb7847-ui/meloader/blob/main/docs/project/handoff.md), [source semantics](https://github.com/jwebb7847-ui/meloader/blob/main/docs/architecture/source-semantics-proposal.md), [local audit](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/local-audit.md), [strict blacklist](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/blacklist-timing.md).
- Existing backlog comments on [PR #11](https://github.com/jwebb7847-ui/mebooru/pull/11#issuecomment-7120527340) and [cosmetic follow-ups](https://github.com/jwebb7847-ui/mebooru/pull/11#issuecomment-7121920974).
- [Meloader performance evidence](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/negative-path-throughput.md), [rating integrity](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/rating-integrity.md).
