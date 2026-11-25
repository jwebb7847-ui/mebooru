# moncommon

Packages monbooru and monloader both carry at `internal/shared/` in each tree:

- `logx` and `contract` now live in the repository-root `shared/` Go module, used by both applications.
- `webui` - the frontend both apps share: htmx, fonts, the base `base.css` and `base.js` served at `/static/shared/`, the `webui/*` templates, the theme tokens the shared files read, and the theme folder loader.

`apitoken`, `bundlepair`, `pairing` and `session` now live in the repository-root `shared/` module, with their regression tests.

`desktop` and `fsx` now live in the repository-root `shared/` module, including platform-specific source and regression tests.
