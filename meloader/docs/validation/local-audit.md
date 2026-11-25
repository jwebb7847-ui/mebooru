# Local audit runbook

Updated with the operator's Apple Silicon Finder acceptance through **2026-11-24**. The original November 14 source-based Audit_A procedure is retained below as **historical, intentionally isolated state**. Verify current listeners, checkout SHAs and config files before use.

## Current Finder build and smoke test (2026-11-24)

### Operator test directory (November 24 onward)

Use `$HOME/Downloads/Projects/Coding/Mebooru` for **all new test source checkouts, build outputs and QA fixtures**. Give each candidate a unique directory under `testing/`, and record both exact Git SHAs. The existing OS-native gallery, media, database and companion runtime state remains in place unless the operator expressly chooses a disposable profile. Never delete or move the real state to reproduce first-run behavior. The older `Documents/VSC` commands below are historical.


This is the simplest workflow for a *fresh development checkout*. It intentionally does not delete/reinitialize the operator's existing databases or override personal configs. To reproduce the **exact accepted PR #11 Mac test**, use Mebooru `8edf75d3be0f12e4b694f036583e727a9e97d4be` and Meloader `8ac2d7077596a72db6012b5f281267afe472c749`. To build **the now-merged feature state**, use Mebooru `2958fed7378801ebe207154efbc5cc285cffaae1`; Meloader `main` moves independently, so explicitly record its SHA if you use it rather than the pinned historical companion.

**Mac — Terminal:**

```sh
go version
git --version
lsof -nP -iTCP:8455 -iTCP:8456 -sTCP:LISTEN
```

If ports are occupied, identify the owning process and config first. Do not start another copy against the same per-user gallery state. When both ports are free, from a terminal with Go and Git installed:

```sh
test_root="$HOME/Downloads/Projects/Coding/Mebooru/testing"
mkdir -p "$test_root"
qa_dir=$(mktemp -d "$test_root/mebooru-merged-qa.XXXXXX") &&
  git clone --branch mebooru-foundation --single-branch https://github.com/jwebb7847-ui/mebooru.git "$qa_dir/mebooru" &&
  git -C "$qa_dir/mebooru" checkout --detach 2958fed7378801ebe207154efbc5cc285cffaae1 &&
  git clone https://github.com/jwebb7847-ui/meloader.git "$qa_dir/meloader" &&
  git -C "$qa_dir/meloader" checkout --detach 8ac2d7077596a72db6012b5f281267afe472c749 &&
  (cd "$qa_dir/mebooru" && sh packaging/build-macos-app.sh "$qa_dir/meloader") &&
  open "$qa_dir/mebooru/dist/Mebooru.app"
```

The copied Finder build uses default per-user state under `~/Library/Application Support/monbooru` and `~/Library/Application Support/monloader` unless overridden. This test does not provide any missing media or DBs. On first run complete Mebooru setup, then in [Meloader Settings → Pairing](http://127.0.0.1:8456/settings#pairing) click **Connect**, and approve in [Mebooru → Settings → Plugins](http://127.0.0.1:8455/settings#plugins). Verify both listeners and the green Mebooru footer connection; **bundled Meloader starts automatically, but pairing is currently manual**. If there is no visible Quit action, close the app normally or send `TERM` to the **identified** `monbooru`/`monloader` listening processes and confirm the ports close.

For the most recent operator's 101-item `default` fixture, check **Settings → Stats** for 95 images and six videos, and Rating for 50 Explicit / 49 General / two Unrated. The `Unrated` row must be a computed search entry, not a database tag. Search `tagcount:rating=0` with Unrated shown to locate those two files. Expected *empty-search* footer counts on unchanged data: Explicit+shown `101/0 hidden`, Explicit+hidden `99/2`, SFW+shown `51/50`, SFW+hidden `49/52`. Change the setting, refresh and verify persistence. The accepted screenshots directly showed Explicit+shown and the rating sidebar; the full combinations are automated expectations. The owner previously reset the database deliberately, so **don't rely on the old MLP/Audit_A contents** being present.

**Strict filter runtime prerequisite.** In Meloader Settings → Advanced inspect the **active gallery-dl version**. The last Mac-tested strict transfer used `1.32.19`. A managed install via system Python 3.9/LibreSSL 2.8.3 emitted `urllib3 NotOpenSSLWarning` on stderr; both probe fixtures exited zero but the strict self-test rejected the warning. The operator rebuilt the managed virtualenv with Homebrew Python 3.13/OpenSSL and subsequently confirmed a real single-post import (one created, 40 tags). [Failure and repair notes](blacklist-timing.md#mac-managed-gallery-dl-ssl-warning-2026-11-24). Do not disable strict blacklist checks to work around this. New gallery-dl releases can be chosen after testing; `1.32.19` is the validated baseline.

**Out of scope for this Finder smoke:** signed/notarized public Mac release, actual Windows/Linux installer trials, auto-pairing and cosmetic UI backlog. See [Mebooru handoff](https://github.com/jwebb7847-ui/mebooru/blob/mebooru-foundation/docs/project/handoff.md).

## Historical November 14 source-based audit setup

## Last validated setup

| Component | Checkout and code checkpoint | Audit configuration |
| --- | --- | --- |
| Mebooru | `$HOME/Documents/VSC/mebooru` · `mebooru-foundation` · `fbb5232` code checkpoint | `$HOME/Library/Application Support/Mebooru Audit/monbooru.toml` · `127.0.0.1:8455` |
| Meloader | `$HOME/Documents/VSC/meloader-alias-qa` · detached at `574fd4e` for the alias Mac check | `$HOME/Library/Application Support/Monloader Audit Main Upgrade/monloader.toml` · `127.0.0.1:8456` |
| gallery-dl | Isolated `1.32.19` executable | `$HOME/Library/Application Support/Meloader Validation/gallery-dl-1.32.19/bin/gallery-dl` |

The repository Go modules declare Go 1.27.4. The last recorded Mac Go version was 1.27.4. The two apps use the upstream command names `monbooru` and `monloader`. Reuse the **Main Upgrade** audit state for the paired check.

The earlier audit checkouts were `$HOME/Documents/VSC/meloader-rating-qa` at `3aa5aca`, `$HOME/Documents/VSC/meloader-item4-qa` at `0b7ff78`, and `$HOME/Documents/VSC/meloader-blacklist-qa` at `eb6edd3` for the Trace prefill check. The original Meloader audit state is in `$HOME/Library/Application Support/Monloader Audit`. See the [blacklist validation notes](blacklist-timing.md#live-mac-acceptance) and the [alias acceptance notes](derpibooru-filing.md#mac-alias-acceptance).

The configured Mebooru galleries include `Audit_A`, `Audit_B`, and `home`. All queue tests here target `Audit_A`. The Mebooru config is authoritative for its media and database paths. The copied audit setup isolates Meloader state and still targets the dedicated Mebooru audit gallery.

## Verify the paired Mac app in Finder

**Mac — Terminal.** Stop any foreground audit servers in their terminals and verify that ports 8455 and 8456 are free using the listener check below. With Git and Go 1.27.4 available, build the verified candidate in a fresh checkout.

```sh
qa_dir=$(mktemp -d "$HOME/Documents/VSC/mebooru-app-qa.XXXXXX") &&
  git clone --branch mebooru-foundation https://github.com/jwebb7847-ui/mebooru.git "$qa_dir/mebooru" &&
  git clone https://github.com/jwebb7847-ui/meloader.git "$qa_dir/meloader" &&
  git -C "$qa_dir/mebooru" checkout --detach 261f6ef60b0ebb184174fde8ff6400798baeb4ba &&
  git -C "$qa_dir/meloader" checkout --detach 8ac2d7077596a72db6012b5f281267afe472c749 &&
  (cd "$qa_dir/mebooru" && sh packaging/build-macos-app.sh "$qa_dir/meloader") &&
  open "$qa_dir/mebooru/dist"
```

**Mac — Finder.** Double-click `Mebooru.app`. Mebooru should open in the browser and launch Meloader. The app looks under `~/Library/Application Support/monbooru` and `~/Library/Application Support/monloader` by default. To validate the custom Audit TOMLs, use the explicit-config procedure in [Launch the paired audit services](#launch-the-paired-audit-services). A fresh default profile may show initial setup and require a new pairing.

**Browser — Mebooru and Meloader.** Finish any Mebooru setup, then use [Meloader → Pairing](http://127.0.0.1:8456/settings#pairing) and approve the request in **Mebooru → Settings → Plugins** if prompted. If the default profile cannot find gallery-dl, set `[gallerydl].binary_path` in the default Meloader TOML to the absolute executable path shown above. Quit Mebooru before editing the TOML and reopen it from Finder. Select a disposable gallery, submit a small known-safe import under **Loader → Queue**, and verify that the completed row has an **Open in gallery** link for an imported item. Quit through Mebooru's Settings and verify both listeners stopped. Reopen from Finder and confirm the pairing and queue history persist.

## Inspect before launch

**VS Code — control terminal.** Check which app currently owns each port.

```sh
lsof -nP -iTCP:8455 -iTCP:8456 -sTCP:LISTEN
```

One row on 8455 means Mebooru is listening and no listener was reported on 8456. If an intended service already owns its port, establish which checkout and config started it before reusing it. When replacing a known foreground audit process, press Ctrl+C in its own terminal, wait for the prompt, and rerun `lsof`. Use `ps -p <PID> -o pid=,comm=` with the observed PID if identification is needed.

**VS Code — each app's terminal.** In the corresponding checkout, inspect `git status --short --branch`, `git rev-parse HEAD`, and `go version`. Fetch `origin` when the test needs a current ref, then choose a clean worktree and a specific revision deliberately. Fetching or changing a checkout does not replace a running executable. Preserve local edits and use a separate worktree for a candidate build.

## Launch the paired audit services

**VS Code — Mebooru terminal.** Start Mebooru first. Leave this terminal running.

```sh
cd "$HOME/Documents/VSC/mebooru" &&
  git status --short --branch &&
  git rev-parse HEAD &&
  test -f "$HOME/Library/Application Support/Mebooru Audit/monbooru.toml" &&
  go run ./cmd/monbooru -desktop=false \
    -config "$HOME/Library/Application Support/Mebooru Audit/monbooru.toml"
```

**Browser — Mebooru.** Open [127.0.0.1:8455](http://127.0.0.1:8455/) and select `Audit_A` in the gallery switcher. The companion connection indicator may update after Meloader starts.

**VS Code — Meloader terminal.** Define and run this function in a separate terminal from Mebooru. It prints the checked-out commit before launch. Verify that the displayed commit matches the test revision, then leave this terminal running. Use the same function after a restart. Pass a checkout path as its first argument to run a different revision with the same audit state.

```sh
run_meloader_upgrade() (
  set -e
  audit_copy="$HOME/Library/Application Support/Monloader Audit Main Upgrade"
  audit_gdl="$HOME/Library/Application Support/Meloader Validation/gallery-dl-1.32.19/bin/gallery-dl"

  cd "${1:-$HOME/Documents/VSC/meloader-alias-qa}"
  git status --short --branch
  git rev-parse HEAD
  for audit_file in monloader.toml gallery-dl.json upgrade-archive.sqlite; do
    if [ ! -f "$audit_copy/$audit_file" ]; then
      printf 'Missing audit file: %s\n' "$audit_copy/$audit_file" >&2
      exit 1
    fi
  done
  if [ ! -x "$audit_gdl" ]; then
    printf 'Missing gallery-dl executable: %s\n' "$audit_gdl" >&2
    exit 1
  fi
  "$audit_gdl" --version

  MONLOADER_SERVER_BIND_ADDRESS=127.0.0.1:8456 \
  MONLOADER_SERVER_BASE_URL=http://127.0.0.1:8456 \
  MONLOADER_MONBOORU_API_URL=http://127.0.0.1:8455 \
  MONLOADER_MONBOORU_WEB_URL=http://127.0.0.1:8455 \
  MONLOADER_MONBOORU_DEFAULT_GALLERY=Audit_A \
  MONLOADER_GALLERYDL_BINARY_PATH="$audit_gdl" \
  MONLOADER_GALLERYDL_CONFIG_PATH="$audit_copy/gallery-dl.json" \
  MONLOADER_GALLERYDL_ARCHIVE_PATH="$audit_copy/upgrade-archive.sqlite" \
  MONLOADER_GALLERYDL_COOKIES_DIR="$audit_copy/cookies" \
  MONLOADER_PTR_ENABLED=false \
  MONLOADER_LOG_LEVEL=info \
    go run ./cmd/monloader -desktop=false -config "$audit_copy/monloader.toml"
)
run_meloader_upgrade
```

**Browser — Meloader.** Open [127.0.0.1:8456](http://127.0.0.1:8456/) and verify that `Audit_A` is selected for the job. Site-specific or job-level routing can affect the destination. Use ordinary download only after confirming the destination and bound.

**Browser — Mebooru.** Check that the footer reports a connection to Monloader. The forward API is Meloader → Mebooru on 8455. Mebooru's reverse `[monloader].api_url` points to `http://127.0.0.1:8456`. Use the existing pairing. A trace that reports `connection refused` for 8455 can still preview outgoing tags but has not tested the gallery's resolution. An occupied 8456 requires identifying the listener before restarting.

The [authenticated Danbooru audit](negative-path-throughput.md#8-live-danbooru-acceptance) used this paired setup. Keep Danbooru credentials in the local audit configuration.

## Check debug-to-Trace prefill

**Browser — Meloader.** After starting a build with PR #8, hard-refresh the [Queue](http://127.0.0.1:8456/queue) with Cmd+Shift+R. Enable **Require blacklist verification before downloading** in **Settings → Rules** if needed. From the queue, expand the existing `Audit_A` job for Derpibooru post `3796956` and choose **[debug] → Trace a post**. Settings should open at Trace with `https://derpibooru.org/images/3796956` in **Post URL** and an empty results area. The lookup starts when you click **Trace**.

## Reproduce the upgrade smoke test

1. **Browser — Meloader.** Open **Settings → Trace a post** and enter `https://derpibooru.org/images/3796956`. The validated trace previewed 19 tags and filled the `Audit_A` column. Check `safe → rating:general` and the artist, character, species, and meta namespaces. This is a metadata preview.
2. **Browser — Meloader.** Submit a fixed, bounded set of five safe-rated posts that have yet to be fetched in the copy. Use a multi-post search for the archive test. For a fixed set, use a Derpibooru search expression such as `safe,(id:3931277 OR id:3931184 OR id:3931173 OR id:3931162 OR id:3931149)` and replace the IDs with five unfetched posts when testing creation. The displayed IDs belong to the [recorded run](negative-path-throughput.md#7-post-merge-derpibooru-upgrade-smoke-test) and should skip in the validated archive. Expect five creations for a fresh set.
3. **Browser — Mebooru.** Open images from that job in `Audit_A`. Check image-specific `rating:general`, categorized tags, the Derpibooru source link, and an original source link where the post provides one. Record post IDs alongside local image IDs.
4. **VS Code — Meloader terminal.** Once the job completes, press Ctrl+C, wait for the prompt, and run `run_meloader_upgrade` again. Keep Mebooru running.
5. **Browser — Meloader.** Refresh the queue. The completed job should be visible immediately. Submit the same fixed search with ordinary Download. Expect `0 created`, `5 skipped`, `0 failed`, and `skipped by download archive` on all five rows. A single-post download can bypass the archive, so use the same multi-post search for both runs.
6. **Browser — Meloader.** Trace `3796956` again. **VS Code — Meloader terminal.** Verify successful enrichment and zero Derpibooru taxonomy HTTP attempts for that warmed trace. A taxonomy cache hit reuses tag category knowledge. The download archive accounts for the skips, and the gallery can reuse an existing tag while associating it with a newly imported image.

## Refresh a copy only when needed

Stop the audit Meloader process before copying. Check for queued, running, or scheduled jobs and review them before launching copied state. Use a new unused destination and retain the source audit directory. Copy the whole state directory, including `monloader.toml`, `profiles/`, `queue.sqlite`, `taxonomy/`, gallery-dl config, and any SQLite sidecars present. Check where the original effective archive is configured, then copy that archive and any corresponding sidecars into the copy as `upgrade-archive.sqlite` with matching sidecar names. Check for absolute paths or symlinks in the copied config, profiles, and extractor options that still reference original state. Review effective routing and keep watches inactive for manual QA. Keep all config and credentials local.

## Recover an interrupted import

**VS Code — Meloader terminal.** After an unexpected stop, restart Meloader with the same config, queue state, and download archive. Keep Mebooru running or start it with the same gallery configuration.

**Browser — Mebooru.** In **Loader → Queue**, locate the interrupted import and check its destination. Use **Retry** to rerun the interrupted window. Meloader bypasses the download archive for that retry because a fetched file may have reached the archive before Mebooru saved it. If strict verification is enabled, the retry checks the source and current blacklist before transferring media. Mebooru can report duplicates for files that were already committed.

For a search, retry resolves the original window again. The source can return a different set if the listing changed. Check the result counts and links in the original gallery before using **Get next**. [Paired-app recovery evidence](loader-integration.md#paired-app-interruption-and-retry)

## Evidence to capture

Record app SHAs, launch modes, tool versions, gallery, source and post IDs, result counts, and relevant redacted logs. A transport reset, HTTP refusal, missing executable, or rate limit must be distinguished from an actual mapping failure. An opt-in live pipeline test may write a synthetic valid-control PNG to `Audit_A`. See the [malformed-rating audit](negative-path-throughput.md#5-malformed-source-rating-against-the-running-audit-gallery) before rerunning it.
