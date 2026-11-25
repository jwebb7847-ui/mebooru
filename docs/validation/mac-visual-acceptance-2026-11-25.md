# Mac visual acceptance — 2026-11-25

## Revisions and disposition

- Mebooru #15: tested `45a864c270336c56fb7cc6da0e881980150cc5f4`; merged `3715067d067417dabeec94c05f1b6bb90148f293`.
- Meloader #25: tested `bd7b6b4addb831ff3711ad6a046abe3b010d94a7`; merged `10542e66537430d21cc295449103bb2832e0693a`.
- Both merges verified closed/merged. No new architecture, storage, download policy or authorization changes.

## Operator acceptance (Apple Silicon)

The operator confirmed all seven fresh-profile checklist items on November 25: build/signing, setup logo, successful setup, automatic pairing, clean empty/search results, clean queue status and sync branding. A screenshot showed the cyan Möbius mark and ordinary setup controls. The operator subsequently confirmed all four existing-profile items: gallery load, image-detail render, hidden/shown Unrated alignment and existing companion connection. Eleven of eleven manual acceptance items passed. Exact generated QA folder suffix was not reported; builds were under the agreed testing root.

## Automated verification

[Native run 45412393411](https://github.com/jwebb7847-ui/mebooru/actions/runs/45412393411): Linux x86-64, Linux ARM64, Windows and Intel macOS passed. Intel Mac also passed signed-bundle lifecycle and first-party pairing/refusal smoke. Hosted macos-15 job cancelled with no recorded steps, so it is not a passed automated check. [Meloader Go run 45412385784](https://github.com/jwebb7847-ui/meloader/actions/runs/45412385784) and [CodeQL run 45412370642](https://github.com/jwebb7847-ui/meloader/actions/runs/45412370642) passed; an older redundant Go run was cancelled.

The operator explicitly requested merge after being informed of the cancellation and confirming local acceptance. No CI requirement was removed. This is a documented manual acceptance exception, not a claim of full hosted green CI. Future changes retain normal verification.

## Next

Repository review and consolidation design; preserve working fallback, original media, licenses/history, user data, and existing behavior. Recurring acquisition is ordinary downloading of directly viewable files, not a separate compressed archival mode.
