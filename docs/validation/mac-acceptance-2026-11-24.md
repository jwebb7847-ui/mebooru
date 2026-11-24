# Mac Finder acceptance and metadata audits — through 2026-11-24

Evidence labels: **operator reported** = local Mac screenshots/Terminal supplied in chat; **CI verified** = repository checks; **code inspected** = implementation behavior, not a live observation. This page records checkpoints, not a guarantee that the personal library or installed app still has the same content.

## Mebooru PR #9: videos and previews

[Merged #9](https://github.com/jwebb7847-ui/mebooru/pull/9) at `2cc1df1`. Static thumbnails and animated WebP-on-hover are selectable in Settings → General. Homebrew `ffmpeg-full` provided FFmpeg/FFprobe with animated WebP support. **Operator reported:** animations worked as desired in the Finder app. **Automated checks:** Linux synthetic MP4/WebM rebuild and static/animated state, JS interactions and cross-compiled binaries. Do not claim the operator manually exercised every failure path.

## Mebooru PR #10: per-gallery Stats

[Merged #10](https://github.com/jwebb7847-ui/mebooru/pull/10) at `946cc21`. **Operator reported:** disposable `Stats_QA` / `Stats_Empty` statistics aligned with the fixture. In `Stats_QA`, one image and one video were present, 91.8 KiB indexed, seven catalog tags, three tags in use, plus process-memory and per-gallery DB/storage data. The fixture used two different media folders with independent DBs. Stats media counts exclude rows marked missing. The current Stats page can list archives as a separate type when present. More detail and SQL example remain in PR #10.

## Meta audit — important distinction, not a request to delete Meta tags

Earlier `default` gallery SQL audit of source vs local Meta tags, restricted to present images, returned:

| Provenance | Distinct Meta tags | One file | Two files | Three-plus files |
| --- | ---: | ---: | ---: | ---: |
| File-derived (`monbooru`) | 10 | 2 | 0 | 8 |
| Derpibooru source-backed | 44 | 29 | 5 | 10 |

Source attribution: `derpibooru` 44 distinct tags / 194 claims; `monbooru` ten / 470. The random long-tail of imported labels makes the sidebar unwieldy, but source labels may be useful in later search. User's approved preference: keep Meta tags and move the category to the bottom using Categories ordering; do not abolish them. These are **historical measurements before the user's intentional database reset**, not counts for the new 101-file gallery.

## Derpibooru rating import verification and local indexing

The first rating issue seemed to show Explicit images in a gallery with only a `general` sidebar entry. SQL audit of the prior snapshot found `default`: 95 Derpibooru items with `general`; `MLP`: one Derpibooru item with `explicit` and 249 locally indexed, unrated files with no recorded Derpibooru source. **Conclusion:** imported ratings appeared to work; local file sync does not assign source ratings. Legacy unrated files were previously visible under SFW because rank -1 passed every upper-ceiling comparison. The earlier discrepancy between 347 footer items and 95 present rows was **not conclusively resolved**; the operator later intentionally reset their database.

During the new Mac import, **operator reported** a fresh `default` gallery with 101 present items (95 images + 6 videos), with 50 Explicit-rated, 49 General-rated and 2 Unrated. `tagcount:rating=0` under Explicit returned precisely two. The tested import used strict blacklist verification, retained source-tag categories, and produced 1,079 catalog tags. Separate fresh single-post example: Derpibooru `3932573` created one file and sent 40 tags after the environment repair described below. A single-post URL never expands to the configured `up to 100` item cap; use a search URL for a batch. Always check the queue's actual `To [gallery]` field.

## Strict filter runtime warning (environment, not an approved policy change)

The user initially encountered `mapping_failed: strict blacklist: gallery-dl could not validate the pre-download filter` even though Trace passed. Managed gallery-dl `1.32.19` was installed in a Python 3.9 virtualenv using LibreSSL 2.8.3. `urllib3` 2 issued `NotOpenSSLWarning` to stderr; both local synthetic filter probes returned exit code 0, but the strict filter self-test treated the stderr warning as failure. The operator installed Homebrew Python 3.13/OpenSSL, rebuilt the managed gallery-dl virtualenv at its established path, and then reported a successful import. Do **not** turn off strict pre-download verification as a workaround. Check `gallery-dl` active version and the Python/OpenSSL backend first.

## Mebooru PR #11: user-visible rating/unrated filter

[Merged #11](https://github.com/jwebb7847-ui/mebooru/pull/11) at `2958fed`; accepted build head `8edf75d`. [Five-platform native CI PASS](https://github.com/jwebb7847-ui/mebooru/actions/runs/45323146804) on Apple Silicon and Intel macOS, Linux x86_64 and ARM64, Windows x86_64. **Operator reported:** the explicit+shown footer displayed `101 total · 101 shown · 0 hidden`, the sidebar listed `explicit 50 / general 49 / unrated 2`, and Stats showed 95 images + 6 videos. Earlier `tagcount:rating=0` gave two results. Operator accepted feature behavior and approved merge.

Code implements independent footer rating-ceiling selection and `unrated: [shown]/[hidden]` selection, including at Explicit; an explicit browser cookie persists the choice. Before an explicit choice, Explicit includes unrated by default, while lower ceilings hide them. A virtual Unrated sidebar row is computed from present unranked rows rather than being saved as a tag. Its link makes unrated visible before searching by `tagcount:rating=0`. Gallery inventory and metadata are unchanged. Visibility is not a security boundary.

If fixture unchanged and search empty:

| Ceiling | Unrated | Shown | Hidden |
| --- | --- | ---: | ---: |
| Explicit | shown | 101 | 0 |
| Explicit | hidden | 99 | 2 |
| SFW | shown | 51 | 50 |
| SFW | hidden | 49 | 52 |

The screenshots directly confirm the first combination and correct sidebar numbers. The other combinations are **code/fixture expectations**, supported by the automated integration test; don't label all four individually screenshot-confirmed.

## Deferred cosmetic and startup work

- Virtual Unrated count shifts left when unrated files are hidden; visually aligns when shown. Counts/function were accepted; styling remains open.
- Empty-results page contains legacy kaomoji `(≖_≖ )` and `(￣～￣;)` in `web/templates/partials/thumbnail_grid.html`.
- Legacy white-haired mascot remains on the initial setup page and another mascot remains in the working/sync indicator.
- Bundled Meloader process startup passed, but manual first-use pairing is still necessary; secure automatic first-party pairing is requested.
- Public signing/notarization and native Windows/Linux installer acceptance have **not** been performed by the operator.

Track follow-ups in the [backlog](../project/backlog.md), not in the merged PR's feature scope.
