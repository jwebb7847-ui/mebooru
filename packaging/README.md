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

From an Apple Silicon checkout, run `sh packaging/build-macos-app.sh` to build `dist/Mebooru.app` with the internal acquisition worker, portable Python, gallery-dl 1.32.19, FFmpeg and ffprobe. The build requires the pinned Go toolchain and Apple Command Line Tools. The app ships its runtime and media dependencies. On first run it seeds the managed profile offline and opens the setup wizard. The local build is ad-hoc signed. [Mac acceptance and current evidence](../docs/validation/self-contained-macos.md)

The default build checks the standalone Python archive, Python wheel hashes and FFmpeg/WebP source hashes. Build caches can supply the same archives through `MEBOORU_MACOS_PYTHON_ARCHIVE` and `MEBOORU_MACOS_MEDIA_ARCHIVES`. To compare an alternative single-file gallery-dl, provide a trusted executable for the target architecture, its SHA-256, and the pinned version's license file.

```sh
MEBOORU_GALLERY_DL="$runtime" \
MEBOORU_GALLERY_DL_SHA256="$trusted_sha256" \
MEBOORU_GALLERY_DL_LICENSE="$runtime_license" \
sh packaging/build-macos-app.sh meloader "$output_dir"
```

In a managed profile, leave `[gallerydl].binary_path` empty to use the bundle. **Settings → Acquisition** can install a qualified update and select the bundled version again. Restart to apply either choice. An absolute configured runtime path remains available. The native workflow also builds a legacy compatibility fixture using `MEBOORU_LEGACY_DESKTOP_BUNDLE=1` for the previous pairing tests. Intel defaults to that legacy bundle until a native managed runtime is qualified.

## Consolidated build-path status

- Active, verified: root `make build-all` and the individual Go modules; native CI builds/tests and smoke starts on Linux x64/ARM64, Windows x64 and Intel macOS; local Apple Silicon Finder bundle and paired lifecycle; Linux x64 tray variants; both CPU Docker **compile stages**. Full end-user Windows/Linux installer trials, complete Docker runtime images and network isolation are not established.
- Historical/unaccepted: standalone portable archives, Windows Inno Setup, Flatpak, AppImage, AUR, GPU Docker variants, Compose and Quadlet examples, and inherited release workflows. Keep them for reference/Chapter VI, not as currently supported distributions. The root release workflow can still publish if invoked on a matching tag or dispatch; **do not create release tags or dispatch it before an approved release milestone**. The nested Meloader workflows do not execute as root GitHub Actions. No release or registry publication is part of II.5.
- `meloader/` is not a standalone checkout: `../shared` is mandatory for the current module. The original frozen repo is the independent rollback source. Do not replace old AUR metadata with an unverified tag. Keep identical packaging scripts and license copies where distinct package formats expect them, rather than create cross-format dependencies merely to remove lines.
