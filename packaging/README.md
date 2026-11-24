# Packaging

| Path | Artifact |
|---|---|
| `icons/` | `monbooru.ico` for Windows shortcuts and the tray, plus the PNG sizes the Flatpak installs into hicolor |
| `flatpak/` | the manifest, the launcher wrapper, the desktop entry and the AppStream metainfo |
| `appimage/` | the AppRun the AppImage runtime executes |
| `monbooru.desktop` | the desktop entry the AppImage carries |
| `aur/` | the PKGBUILD the AUR package is stamped from, and the licence for those packaging sources |
| `windows/` | the Inno Setup script; the workflow stages the installer's own copy of the archive's contents under `windows/payload/` first |
| `release-env.sh` | the version and the ldflags every artifact is stamped with |
| `fetch-tools.sh` | downloads the ffmpeg and ONNX Runtime the bundled build carries, for one target, into `tools/` |
| `build-binaries.sh` | assembles the portable archives into `dist/`, one shape per run. Each carries an empty `monbooru.toml`, which is what makes unpacking one keep its data in the folder |
| `build-installer.sh` | runs ISCC over one staged payload to produce a Windows installer |
| `flatpak-bundle.sh` | installs the runtime and builds the Flatpak bundle; must run unprivileged |
| `build-appimage.sh` | builds the bundled AppImage for one target, from the same `tools/` |
| `appimage-core.sh` | sourced by `build-appimage.sh`: fetches the pinned appimagetool and runtime, adds the desktop entry and icons and packs the AppImage. Kept identical to the sibling app's copy |
| `build-aur.sh` | stamps the PKGBUILD with the version and the tag tarball's checksum, and generates its `.SRCINFO` |

## Current Mebooru Finder candidate

The fork's macOS bundle build script is `packaging/build-macos-app.sh`. From a Mebooru checkout on `mebooru-foundation`, pass a local Meloader checkout as its argument, then open `dist/Mebooru.app` in Finder. The app packages the two executable services and launches the companion; it **does not yet automatically establish the trusted pairing** in a new profile. See [Mac runbook](https://github.com/jwebb7847-ui/meloader/blob/main/docs/validation/local-audit.md) and [dated acceptance](../docs/validation/mac-acceptance-2026-11-24.md).

The November 24 accepted commit is `2958fed`, Mebooru PR #11. The previously tested installed app used the PR head `8edf75d`. The local bundle is ad-hoc signed, **not** a signed/notarized public release. Windows/Linux artifacts in the table above are inherited packaging infrastructure; source builds and cross-platform CI do not prove their installation UX. Retain legacy identifiers where renaming would break persisted installs.
