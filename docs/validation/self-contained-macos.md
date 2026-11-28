# Self-contained Apple Silicon validation

Use candidate `3c7f5cb59cbd44c5c83cae37c18a6eb607b71300` from [codex/self-contained-runtime](https://github.com/jwebb7847-ui/mebooru/tree/codex/self-contained-runtime). The Chapter II live acceptance candidate remains `c2f53f216a4c91b771bc5ae0051c4a718700b33b`. The Apple Silicon app is ad-hoc signed for local validation.

## Open an isolated profile

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

## Development evidence

| Runtime or workflow | Observed result |
| --- | --- |
| Earlier one-file gallery-dl 1.32.19 | Version process 6.25 s. Loopback metadata and transfer took about 46 s. |
| Bundled portable Python and gallery-dl 1.32.19 | Version process 0.05 s. First Settings page 0.319 s in the comparison. Offline release through one loopback import took 1.851 s. |
| Final signed bundle with the verified 1.32.21 wheel | Version process 0.08 s. Fresh setup wizard 0.106 s and a loopback import 3.889 s in the update run. |

- The app checked PyPI and installed the hash-pinned wheel, then used it in contained loopback imports with the strict predicates, duplicate detection, and a Settings blacklist edit that excluded the post before staging. Offline update refusal, offline rollback, Quit and restart with the same queue and media passed. A corrupted update was rejected while the offline original remained readable. Source classification covered Derpibooru, Danbooru and e621. The public Derpibooru endpoint returned HTTP 501 for the updated runtime and the baseline HTTP client, so there is no new live-source pass for this bundle.
- A held loopback update check had its connection closed when acquisition switched offline.
- Synthetic MP4, WebM and animated GIF acquisitions preserved original bytes, dimensions and ratings and generated JPEG thumbnails and animated WebP previews. The packaged tools ran with a system-only `PATH`. The rebuilt ZIP unpacked with a valid ad-hoc signature.
- A prior managed disposable profile reopened on the new binaries with byte-identical TOML and media, and retained its offline setting, queue and watch. The owner profiles and legacy standalone queue migration were outside this test.
- macOS LaunchServices opened the unpacked, signed ZIP in a disposable profile. First run, offline status and Settings Quit passed. The bundle declares macOS 13 as its minimum. The minimum OS and public download Gatekeeper flow still require platform acceptance.
