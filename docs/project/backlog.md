# Mebooru / Meloader outstanding work

**Reviewed:** 2026-11-25. This is a *prioritized next-step proposal*, not a declaration that these features are implemented. Link new PRs and update statuses after acceptance. The working Mebooru branch is `mebooru-foundation`; Meloader develops from `main`.

## Completed — first-party bundled Meloader ready on launch

[Mebooru #13](https://github.com/jwebb7847-ui/mebooru/pull/13) and [Meloader #24](https://github.com/jwebb7847-ui/meloader/pull/24) merged; see [pairing design and closeout](bundled-pairing.md) and [Mac acceptance](../validation/mac-acceptance-2026-11-24.md). Final hosted Apple Silicon runner was outstanding at merge, accepted explicitly by the operator based on successful local Mac testing and the runner-allocation failures. This is not a green hosted CI result.

## Next major milestone — unified product architecture

[Approved direction](unified-product-direction.md): consolidate repositories with history/licensing preserved; keep a separate acquisition worker, one UI and one application lifecycle, hybrid on-demand/scheduled operation while app is open, explicit offline switch. Plan migration and compatibility gates before implementation.

- Replace the nested Meloader Settings page with ordinary Mebooru settings. First-party acquisition should not appear as an external plugin users can accidentally remove; provide clear enable/disable and recovery controls. Preserve explicit external plugin approval.
- Bundle a self-contained gallery-dl runtime with managed updates, validated dependencies, strict pre-download verification and rollback; no manual Python/Homebrew requirement for ordinary use.
- Expose deterministic recurring acquisition watches for selected booru sources and character queries: discover new posts, filter before download, retain source tags and provenance, route to chosen gallery as ordinary directly viewable media. No separate archive feature or compression. No agentic behavior or requirement to operate while app is closed. Investigate reuse of existing Watches.
- Add prominent Quit Mebooru control that drains work and stops owned services, without shutting down external services indiscriminately.

## Completed — focused visual rebranding

[Mebooru #15](https://github.com/jwebb7847-ui/mebooru/pull/15) and [Meloader #25](https://github.com/jwebb7847-ui/meloader/pull/25) merged on November 25. The operator confirmed all seven fresh-profile checks and four existing-library checks: setup and sync branding, clean empty/search and queue text, automatic pairing, gallery/detail rendering and Unrated alignment. Default legacy mascot display and decorative kaomoji were removed; functional symbols, custom themes, opt-in legacy themes and compatibility-critical identifiers are retained. See [visual acceptance](../validation/mac-visual-acceptance-2026-11-25.md).

Remaining historical identifiers/assets are not an open defect in this scope; inventory them during repository consolidation and preserve compatibility. Do not delete custom or legacy artwork indiscriminately.

## Priority 3 — usability, provenance and workflows

- **Meta category:** source Meta labels are useful for search but overpopulate the sidebar. The November sample found 44 Derpibooru-backed Meta names (29 appearing on one file, five on two, ten on three or more) versus ten file-derived Meta names. User prefers **retaining** these tags. Manual workaround: drag Meta to the bottom in **Categories** (order persists per gallery). Discuss any future hide/collapse/low-use threshold before implementing; do not discard provenance.
- **Gallery semantics:** media subfolders inside one gallery are not auto-created galleries. This was clarified as intended behavior, not a defect. Revisit only if the user explicitly chooses a folder-to-gallery workflow.
- **Unrated classification/recovery:** PR #11 now *filters* unrated images; it does not infer ratings or backfill them. A future opt-in source hash lookup could recover authoritative ratings for local files. Never infer `explicit` or `general` from folder names or modify a user's manual rating silently.
- **gallery-dl upgrades:** `1.32.19` is a tested strict-blacklist baseline, not forever pinned for convenience. Validate newer releases through synthetic strict filter self-tests, Derpibooru/e621/Danbooru and real bounded imports before updating recommendations. Consider a clearer error when Python startup warnings break preflight; keep the fail-closed behavior and don't relax blacklists to mask warnings.
- **Packaging:** local Finder build is ad-hoc signed and Mac-tested. Public macOS distribution still needs signing/notarization; Windows and Linux installer packaging require actual platform acceptance beyond CI build/run verification. Decide packaging/distribution when functional requirements are met.

## Verification and workflow guardrails

Keep Mebooru core offline, use Meloader for WAN; preserve rating/source evidence, alias matching, user overrides and strict blacklist safety. Use independent galleries, metadata-first import and database-first storage. Run appropriate Go tests, race/vet/CI and security review before merging. The operator can test Mac Finder behavior and provide screenshots but has no direct Windows/Linux hardware access; label those as automated verification, not hands-on validation. Record the actual branch SHA and maintain docs at each milestone.

Sources: [dated Mac acceptance](../validation/mac-acceptance-2026-11-24.md), [PR #11 backlog notes](https://github.com/jwebb7847-ui/mebooru/pull/11#issuecomment-7120527340), [cosmetics](https://github.com/jwebb7847-ui/mebooru/pull/11#issuecomment-7121920974), and [Meloader roadmap](https://github.com/jwebb7847-ui/meloader/blob/main/ROADMAP.md).
