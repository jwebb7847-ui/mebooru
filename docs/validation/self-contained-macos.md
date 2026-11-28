# Self-contained Apple Silicon validation

Use candidate `e38bbcafb754dac58e2a2a06ceb1c2402058ee62` from [codex/final-qualification](https://github.com/jwebb7847-ui/mebooru/tree/codex/final-qualification). The Chapter II live acceptance candidate remains `c2f53f216a4c91b771bc5ae0051c4a718700b33b`. The Apple Silicon app is ad-hoc signed for local validation. The local ZIP is `dist/mac-arm64-e38bbca/Mebooru-macos-arm64.zip` with SHA-256 `f2b72622384837a69531f370428006f22277a92b12bea214dbf272a4872d2170`.

## Open an isolated profile

From the repository root, verify the download and extract it into a temporary folder.

```zsh
zip="$PWD/dist/mac-arm64-e38bbca/Mebooru-macos-arm64.zip"
printf '%s  %s\n' f2b72622384837a69531f370428006f22277a92b12bea214dbf272a4872d2170 "$zip" | shasum -a 256 -c && \
  unpacked="$(mktemp -d "${TMPDIR:-/tmp}/mebooru-app.XXXXXX")" && \
  ditto -x -k "$zip" "$unpacked"
app="$unpacked/Mebooru.app"
codesign --verify --deep --strict "$app"
```

In a separate macOS test account, extract the ZIP and open **Mebooru.app**. Complete the setup wizard with a disposable gallery, then open **Settings → Acquisition**. It should show gallery-dl **1.32.19 · Ready** and acquisition offline. Meloader runs internally. Opening, setup and use require no installed Python, gallery-dl, ffmpeg or companion app.

For a disposable profile in the same macOS account, explicitly pass a new config path. The app keeps that profile's data and logs beside the config and skips account-wide desktop integration. Set `app` to the extracted bundle path before running the commands.

```zsh
qa="$(mktemp -d "${TMPDIR:-/tmp}/mebooru-acceptance.XXXXXX")"
mkdir "$qa/gallery"
port="$("$app/Contents/Resources/runtime/python/bin/python3" -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
MEBOORU_SERVER_BIND_ADDRESS="127.0.0.1:$port" \
  "$app/Contents/MacOS/monbooru" -config "$qa/monbooru.toml"
```

Keep the same profile and port on restart. For a development build, run `sh packaging/build-macos-app.sh meloader "$output_dir"` from the exact candidate checkout on Apple Silicon. [Build inputs and overrides](../../packaging/README.md)

## Acceptance steps

1. With acquisition offline, confirm **Check for updates** and **Install qualified update** are unavailable. Verify the library remains usable.
2. Go online. In **Loader**, import a known general-rated public Derpibooru still with a one-post cap and repeat it. Check its tags, rating and duplicate result. Keep the strict blacklist enabled.
3. In **Settings → Acquisition**, select **Check for updates** and then **Install qualified update**. On restart, Settings should report **1.32.21 · Ready**. Repeat the bounded import when Derpibooru is responding.
4. Go offline. Select **Use bundled version** and restart. Confirm **1.32.19 · Ready**, offline status and the unchanged gallery and queue. Quit through Settings.
5. In **Loader → Watches**, choose a destination, run a manual check, then try and pause an interval. Watch results show counts. When a check is queued or running, use **Cancel check**. In **Loader → PTR**, sync only if a disposable repository is configured. Confirm local lookup after going offline.

## Migrate a legacy profile and verify rollback

1. Quit both old apps. Keep the previous app and back up the library profile, gallery media and database files, and the standalone Meloader configuration and state. Take the backup with both apps stopped.
2. Open the candidate with an isolated copy of the library. Use a separate macOS test account or a disposable profile whose gallery and database paths already point to copies. A copied TOML may still reference its original files. Check the configured paths before opening it.
3. In **Settings → Acquisition → Import a Meloader profile**, enter the copied Meloader TOML path. Leave the state folder blank for the normal layout beside the TOML or under its `data` folder. Supply it explicitly for another layout. Leave both fields blank for a library-only profile. Confirm the old worker is stopped and import. Mebooru restarts offline.
4. Verify the existing originals, personal tags, ratings, notes, collections and provenance. Confirm old jobs and watches in Loader, the strict blacklist in Settings, and a known PTR hash when an index was copied. Unfinished jobs show as **Interrupted** and can be explicitly retried if they were ordinary supported downloads. Check credentials before going online. Source options that require attention appear under **Source credentials and options**.
5. To roll back, quit the candidate, save the newer `monbooru.toml` separately, and restore the whole `managed-acquisition/backup/monbooru.toml` file to its original location. Open the previous app with the original standalone Meloader files. Keep the new managed state and imported media available for a later return. The migration fixture passed this reversal.

The copy includes SQLite-consistent snapshots of the queue, download archive and PTR indexes, and copies of the policy, source profiles and cookie settings. The original gallery and library database stay in place. The copied archive is retained as history. Managed duplicate checks use actual library and media evidence, so an already downloaded post can be fetched again without creating another library record. Unsupported legacy source URLs, raw gallery-dl configuration and legacy folder rules still need a supported equivalent. The app refuses raw configuration and folder-rule migration before switching the library profile. Unbounded watches become manual bounded checks.

## Development evidence

| Runtime or workflow | Observed result |
| --- | --- |
| Earlier one-file gallery-dl 1.32.19 | Version process 6.25 s. Loopback metadata and transfer took about 46 s. |
| Bundled portable Python and gallery-dl 1.32.19 | Version process 0.05 s. Baseline loopback import 1.851 s in the comparison. |
| Final signed package with verified 1.32.21 wheel | Fresh setup wizard 0.108 s. Loopback single import 4.497 s in the update test. |

- The exact signed package opened through macOS LaunchServices from its unpacked ZIP. Fresh setup, bundled runtime, offline default and Settings Quit passed. The app declares macOS 13 as its minimum. The local hardware test ran on a newer Apple Silicon Mac.
- With bundled 1.32.19, the exact package imported a live public general-rated Derpibooru still, detected the repeat and reopened the original offline after Quit. The immediately preceding code completed the full three-post live sequence, including blacklist editing, active cancel and retry. Earlier HTTP 501 stopped the prior turn's live retest. The endpoint responded during this qualification.
- The exact package verified and installed the pinned wheel from PyPI and passed contained loopback import, duplicate and edited-blacklist checks, offline rollback and restart. A corrupt update left the original available offline.
- Synthetic MP4, WebM and animated GIF imports preserved original bytes, dimensions and ratings and generated JPEG thumbnails and animated WebP previews. Watches passed per-gallery delivery, manual and scheduled checks, pause, duplicate reporting, active cancellation and restart. PTR passed explicit sync, transport drain when going offline and cached lookup across restart.
- The packaged migration fixture kept old media and source data unchanged, preserved the library note, tag, rating and collection, retained the queue, archive, watch and PTR lookup, and retried the migrated job. An authenticated Derpibooru search fixture verified the saved key and filter during managed acquisition. Rotated credentials were used after restart and stayed out of Settings output. Retired first-party API credentials were refused and the unrelated scoped read token still worked.

## Personal-use release checklist and PR #38

- Verify the ZIP hash above and run the fresh setup and migration acceptance steps. The two protected disposable profiles remain separate owner checks. Local ad-hoc signing is sufficient for the personal-use package.
- Review the exact [native matrix](https://github.com/jwebb7847-ui/mebooru/actions/runs/46153485744) and [CodeQL scan](https://github.com/jwebb7847-ui/mebooru/actions/runs/46153488608). [Current platform and security checkpoint](../project/cold-start.md)
- Keep the exact prior package `3c7f5cb59cbd44c5c83cae37c18a6eb607b71300`, the successful Chapter II live candidate `c2f53f216a4c91b771bc5ae0051c4a718700b33b`, and the pre-migration profile backup. Developer ID and notarization can follow separately.
- After the owner's approval, fast-forward the head of [draft PR #38](https://github.com/jwebb7847-ui/mebooru/pull/38) from `360113279069d0804a6d44076eef1e609ded0380` to the verified candidate. That head is an ancestor of `e38bbcafb754dac58e2a2a06ceb1c2402058ee62`. Review the exact diff and CI evidence before requesting a separate readiness or merge decision.
