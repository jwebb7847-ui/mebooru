# Mebooru / Meloader outstanding work

**Reviewed:** 2026-11-24. This is a *prioritized next-step proposal*, not a declaration that these features are implemented. Link new PRs and update statuses after acceptance. The working Mebooru branch is `mebooru-foundation`; Meloader develops from `main`.

## Priority 1 — first-party bundled Meloader ready on launch (not implemented)

**Observed problem:** Finder `Mebooru.app` launches bundled Meloader, but a new/cleared profile still shows `Connect Meloader in Settings → Plugins` until an operator visits Meloader Settings, initiates pairing and manually approves it in Mebooru. The desired finished product needs no convoluted initialization.

**Design:** Auto-bootstrap trusted, **bundled, locally launched, first-party** companion pairing on first launch, including the correct local URLs and durable connection. Retain the separate WAN-capable process and Mebooru's WAN-isolated core. **Do not** silently approve unknown external plugins or arbitrary hosts, expose tokens to frames, or weaken ability to revoke/pause access. Discuss trust boundaries and recovery before implementation.

**Acceptance:** Fresh macOS install; existing profile; full/partial profile reset; restart; external existing Meloader; occupied port; crashed companion; upgrade and revocation; paired API and embedded Loader tab; no unexpected outbound Mebooru access. CI on Linux/Windows/macOS plus Finder Mac owner test. Implementation pointers: `cmd/monbooru/meloader.go`, pairing code in both repos, embedded Loader panel, companion lifecycle tests.

## Priority 2 — finish visual rebranding and cleanup

- Replace legacy Monbooru white-haired mascot on first-run `/setup` (default theme uses `themeMascots(theme)["hello"]`, with rendering in `web/templates/setup.html`). Respect custom themes and fallbacks.
- Replace legacy top-right/sync/working mascot (job status uses `themeMascots(... )["sparkle-head"]`; inspect job status templates). Avoid functionality changes.
- Remove kaomoji/emoticons from empty-results and degraded screens: `web/templates/partials/thumbnail_grid.html` contains `(≖_≖ )` and `(￣～￣;)`. Keep useful explanatory copy and recovery hints.
- Align the virtual `Unrated` count with other Rating counts when the setting is hidden. The virtual row has a form/button rather than a conventional tag link in `web/templates/partials/sidebar_content.html`. Functionality and counts passed; this is cosmetic.
- Review other user-visible old `mon` prefix branding **without renaming compatibility-critical binaries, Go packages, API/TOML keys, or persisted paths merely for appearance**. A focused inventory and cross-platform regression should accompany a future rebrand.

These were deliberately deferred from [PR #11](https://github.com/jwebb7847-ui/mebooru/pull/11). Confirm expected visuals before making a design change.

## Priority 3 — usability, provenance and workflows

- **Meta category:** source Meta labels are useful for search but overpopulate the sidebar. The November sample found 44 Derpibooru-backed Meta names (29 appearing on one file, five on two, ten on three or more) versus ten file-derived Meta names. User prefers **retaining** these tags. Manual workaround: drag Meta to the bottom in **Categories** (order persists per gallery). Discuss any future hide/collapse/low-use threshold before implementing; do not discard provenance.
- **Gallery semantics:** media subfolders inside one gallery are not auto-created galleries. This was clarified as intended behavior, not a defect. Revisit only if the user explicitly chooses a folder-to-gallery workflow.
- **Unrated classification/recovery:** PR #11 now *filters* unrated images; it does not infer ratings or backfill them. A future opt-in source hash lookup could recover authoritative ratings for local files. Never infer `explicit` or `general` from folder names or modify a user's manual rating silently.
- **gallery-dl upgrades:** `1.32.19` is a tested strict-blacklist baseline, not forever pinned for convenience. Validate newer releases through synthetic strict filter self-tests, Derpibooru/e621/Danbooru and real bounded imports before updating recommendations. Consider a clearer error when Python startup warnings break preflight; keep the fail-closed behavior and don't relax blacklists to mask warnings.
- **Packaging:** local Finder build is ad-hoc signed and Mac-tested. Public macOS distribution still needs signing/notarization; Windows and Linux installer packaging require actual platform acceptance beyond CI build/run verification. Decide packaging/distribution when functional requirements are met.

## Verification and workflow guardrails

Keep Mebooru core offline, use Meloader for WAN; preserve rating/source evidence, alias matching, user overrides and strict blacklist safety. Use independent galleries, metadata-first import and database-first storage. Run appropriate Go tests, race/vet/CI and security review before merging. The operator can test Mac Finder behavior and provide screenshots but has no direct Windows/Linux hardware access; label those as automated verification, not hands-on validation. Record the actual branch SHA and maintain docs at each milestone.

Sources: [dated Mac acceptance](../validation/mac-acceptance-2026-11-24.md), [PR #11 backlog notes](https://github.com/jwebb7847-ui/mebooru/pull/11#issuecomment-7120527340), [cosmetics](https://github.com/jwebb7847-ui/mebooru/pull/11#issuecomment-7121920974), and [Meloader roadmap](https://github.com/jwebb7847-ui/meloader/blob/main/ROADMAP.md).
