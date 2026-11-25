# Mebooru and Meloader — historical project handoff

**Snapshot 2026-11-24; superseded by [MEBOORU.md](../../../MEBOORU.md).** The current [development workflow](../../../MEBOORU.md#development-workflow) uses one repository and milestone PRs. The old two-repository instructions, fixture counts and next actions below are historical evidence.

## Start with these documents

| Document | Why it matters |
| --- | --- |
| [Mebooru handoff](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/project/handoff.md) | **Primary next-chat entry**: merged work, current Mac fixture, code SHAs, next implementation |
| [Mebooru prioritized backlog](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/project/backlog.md) | Approved/recommended next work and validation gates |
| [November 24 Mac acceptance](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/validation/mac-acceptance-2026-11-24.md) | Source-rated vs local-unrated findings, PRs #9–#11, strict filter environment repair |
| [Meloader roadmap](../../ROADMAP.md) | Milestones, invariants, completed acquisition tests, open work |
| [Local runbook](../validation/local-audit.md) | Current Finder build and historical source-based launch setups |
| [Source semantics contract](../architecture/source-semantics-proposal.md#product-decisions) | Rating, taxonomy, source authority, provenance, filing choices |
| [Blacklist timing](../validation/blacklist-timing.md), [rating integrity](../validation/rating-integrity.md), [performance evidence](../validation/negative-path-throughput.md) | Strict verification, live acquisition, performance, explicit failure paths |
| [e621 validation](../validation/e621.md) and [Derpibooru filing/aliases](../validation/derpibooru-filing.md) | Source-specific mapping and operator-approved decisions |

## Repository and operator state — verify heads again before changes

- **Mebooru**: `jwebb7847-ui/mebooru`; development branch **`mebooru-foundation`**, observed at merge commit **`2958fed7378801ebe207154efbc5cc285cffaae1`** after [PR #11](https://github.com/jwebb7847-ui/mebooru/pull/11). The repository's `main` is the upstream v1.24.0-based baseline, **not** the current feature branch. Previous accepted [PR #9](https://github.com/jwebb7847-ui/mebooru/pull/9) provides video static/animated preview choice and FFmpeg support; [PR #10](https://github.com/jwebb7847-ui/mebooru/pull/10) adds Stats.
- **Meloader**: `jwebb7847-ui/meloader`; development branch **`main`**, observed at **`1a0cef7a3b585a59eaa33592905d68e51e556bf8`** at refresh time. The prior paired Finder Mac test deliberately built Meloader revision **`8ac2d7077596a72db6012b5f281267afe472c749`**; **do not equate that historical test revision with the present head**.
- **Mac setup**: Apple Silicon MacBook Air, Go **1.27.4**, Homebrew `ffmpeg-full`, historical managed gallery-dl **1.32.19** on Homebrew Python 3.13/OpenSSL after a LibreSSL warning failure. Mebooru listens at `127.0.0.1:8455` and Meloader at `127.0.0.1:8456`. Mebooru app candidate on operator Mac: `~/Applications/Mebooru-PR11-8edf75d.app`; its installed binary remains the tested PR head until explicitly rebuilt/replaced. Service configs/data have historically lived under `~/Library/Application Support/monbooru` and `~/Library/Application Support/monloader`; **read the current configuration and listeners rather than guessing exact live paths**.
- **Recent operator-run acceptance**: Finder Mebooru + bundled Meloader successfully launched and paired manually, and actual Derpibooru jobs completed. After intentional database reset, the `default` gallery had **101 present items** (95 images, six videos) and 1,079 catalog tags. Source rating breakdown: 50 Explicit, 49 General, **two local Unrated** (confirmed by `tagcount:rating=0`). Operator accepted PR #11 visual and filtering behavior; five cross-platform CI jobs passed. The prior `MLP` 249-unrated snapshot was **before reset** and is not today's inventory.

## Approved architectural invariants

1. Mebooru local core remains WAN-isolated; Meloader is the separate, WAN-capable acquisition and metadata-recovery service. Its embedded Loader surfaces do not turn the library into a network client.
2. Meloader has **one generic gallery-dl / queue / verify / download / map / upload pipeline**, with source adapters for taxonomy/rating evidence and broad profile fallback. Unknown required category/rating evidence must fail visibly, not silently flatten or invent data. Keep provenance and source tags.
3. Strict pre-download blacklist verification is user-configurable and **fail closed** for unsupported/unverifiable items. It screens source metadata and aliases, not the image pixels; safe-rated material can contain prohibited subjects and Explicit can be allowed when nonviolent.
4. Source metadata is trusted over AI, and local synced images stay unrated until a user or verified source assigns a rating. PR #11's visibility toggle **does not classify, retag, or delete** those files.
5. Libraries are independent named gallery configurations with separate databases and folders. Subfolders under one gallery do not become independent galleries. Loader jobs retain their selected destination; inspect the queue's `To [gallery]` field.
6. Preserve compatibility-critical legacy `monbooru`/`monloader` module paths, config/TOML keys, cookies, command directories and local state. User-facing branding should use Mebooru/Meloader. Discuss behavior/privacy/trust/product changes before implementing them.

## What is done and what is next

Completed: Derpibooru source taxonomy and batched cache resolution, e621 and Danbooru mapping/rating cases, approved `lore → meta` and `contributor → artist`, verified alias matching, strict blacklist transfer validation, bounded bulk/restart recovery, Loader integration, Finder launch with manual pairing, Mac video-preview test, Stats, and unrated filtering.

**Next recommended product PR:** securely initialize and **auto-pair the known bundled Meloader companion** during Mebooru first run, so users need not open two Settings pages. Running the Meloader process on app open is already implemented; trust bootstrap is not. This **must not auto-authorize arbitrary external or remotely hosted plugins**, relax the local/WAN privacy boundary, or leak tokens to browser frames. Include fresh-profile/restart/revocation/crash/port-conflict acceptance. Then complete remaining mascot and empty-state branding, Unrated count alignment, and packaged release work. See [the exact backlog](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/project/backlog.md).

Other important cosmetic/experience notes: move Meta below more useful tag categories through existing **Categories** ordering; **do not delete** obscure Meta source labels. A previous audit found 44 source-backed Meta names vs ten file-derived, many used by only one item. The virtual Unrated count shifts horizontally when unrated is hidden (cosmetic). The empty-results UI still uses kaomoji. New gallery-dl releases require controlled strict-filter validation; 1.32.19 is a known-good **test baseline**, not a perpetual pin policy. Public macOS signing/notarization and Windows/Linux installer UX remain outstanding.

## Debugging and safety-critical incident

A Mac queue job failed `mapping_failed: strict blacklist: gallery-dl could not validate the pre-download filter` although Trace passed. The **managed gallery-dl 1.32.19** executable ran under Python 3.9's LibreSSL 2.8.3; `urllib3` v2 printed `NotOpenSSLWarning` on stderr. Two synthetic predicate probes exited status 0, but the filter self-test considered stderr a failure. Rebuilding its virtual environment using Homebrew Python 3.13/OpenSSL resolved it; operator then imported Derpibooru post `3932573` (one created, 40 tags). **Never advise disabling strict verification to bypass an environment diagnostic.**

## Interaction and acceptance process

- Use explicit headings such as **Mac Terminal**, **Browser — Mebooru**, and **Browser — Meloader**; provide paste-ready commands and expected outputs. Inspect ports (`lsof -nP -iTCP:8455 -iTCP:8456 -sTCP:LISTEN`), processes, Git checkout SHAs, active app bundle, selected gallery and config before starting or killing anything.
- The operator can test on Mac only. Cross-platform CI confirms core tests/build and some lifecycle behavior, but is not equivalent to hands-on native Linux/Windows installer acceptance. Label actual Mac screenshots versus synthetic fixture results accurately.
- New feature work uses a separate branch and PR; audit and merge only after CI and requested Mac validation. Changes to trust, privacy, rating semantics or visible workflows must be discussed.
- Do not copy actual personal media, tokens, raw private TOMLs, session material, or credentialed logs into the repo. Preserve all databases and user state unless explicitly instructed otherwise.
- The [Meloader local runbook](../validation/local-audit.md) includes both the current Finder procedure and the old Audit_A setup, separated by date. **GitHub merges do not update a running Mac binary.**

## Historical milestone references

[Meloader Item 3 PR #5](https://github.com/jwebb7847-ui/meloader/pull/5), [Item 4 PRs #6–8](https://github.com/jwebb7847-ui/meloader/pull/8), [Meloader filing PR #10](https://github.com/jwebb7847-ui/meloader/pull/10), [alias PR #11](https://github.com/jwebb7847-ui/meloader/pull/11), [Meloader setup/PTR hardening PR #20](https://github.com/jwebb7847-ui/meloader/pull/20), [Mebooru packaging PR #7](https://github.com/jwebb7847-ui/mebooru/pull/7), [Mebooru PR #11](https://github.com/jwebb7847-ui/mebooru/pull/11). Consult original linked validation notes for each code SHA and measured result; don't collapse historical work into the current fixture.
