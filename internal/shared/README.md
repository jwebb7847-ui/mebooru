# moncommon

Packages monbooru and monloader both carry at `internal/shared/` in each tree:

- `desktop` - the per-OS layer: the `-desktop` profile's paths, single instance, menu entry and tray, and what headless code needs from the OS too (disk usage, console hiding, the Host guard).
- `logx` and `contract` now live in the repository-root `shared/` Go module, used by both applications.
- `fsx` - the atomic and durable file writes.
- `webui` - the frontend both apps share: htmx, fonts, the base `base.css` and `base.js` served at `/static/shared/`, the `webui/*` templates, the theme tokens the shared files read, and the theme folder loader.

`apitoken`, `bundlepair`, `pairing` and `session` now live in the repository-root `shared/` module, with their regression tests.
