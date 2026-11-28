# Managed Loader acceptance on Apple Silicon

Use Chapter II candidate `c2f53f216a4c91b771bc5ae0051c4a718700b33b`. It adds an early cancellation fix to `352586d4dfdc94966f6ba5daaae839634fb7136d`, which includes archive reopening, duplicate and publication fixes, and library startup if acquisition requires repair. Run the checkout commands from your Mebooru repository.

```zsh
git fetch origin codex/ii6-live-acceptance
qa_checkout="$(mktemp -d "${TMPDIR:-/tmp}/mebooru-acceptance.XXXXXX")"
git worktree add --detach "$qa_checkout/source" c2f53f216a4c91b771bc5ae0051c4a718700b33b
cd "$qa_checkout/source"
```

## Reopen the two preserved profiles

Close any instances using either profile before reopening them. Build both executables into a new directory. The original LiveTest profile already completed the post-reboot ownership repair, so try ordinary startup first.

```zsh
test_root="$HOME/Downloads/Projects/Coding/Mebooru/testing"
qa="$test_root/ii6-live.jryLAd"
fixed="$(mktemp -d "$test_root/ii6-publication.XXXXXX")"
export GOWORK=off GOTOOLCHAIN=local
go build -mod=readonly -o "$fixed/monbooru" ./cmd/monbooru &&
  (cd meloader && go build -mod=readonly -o "$fixed/monloader" ./cmd/monloader) &&
  "$fixed/monbooru" -config "$qa/monbooru.toml"
```

Confirm the original image opens and job history is visible in Loader. If Loader says acquisition is unavailable, use the library normally offline and keep the terminal line starting with `acquisition unavailable`. Keep the profile and staging records in place. If the only reported problem is an invalid cleanup receipt, the previous [post-reboot ownership repair procedure](https://github.com/jwebb7847-ui/mebooru/blob/4ad88c441baa794101393d666112cefadf9b25b2/docs/validation/managed-live-loader.md#recover-the-preserved-livetest-profile) also applies to these binaries.

Quit the first profile before opening the second.

```zsh
qa="$test_root/ii6-fresh.HhTP7s"
"$fixed/monbooru" -config "$qa/monbooru.toml"
```

Confirm its already imported images and job history. Preserve the unresolved staging records from the earlier failures. If that state still prevents a repeat, keep the new `acquisition owner:` or `retained unresolved publication` terminal line and use the fresh-profile commands below for the live sequence.

## Start with a fresh disposable gallery

Run the following from the candidate checkout for a repeatable live check without prior failed jobs. It builds both executables, installs the pinned runtime and creates an isolated profile that starts offline.

```zsh
test_root="$HOME/Downloads/Projects/Coding/Mebooru/testing"
mkdir -p "$test_root"
qa="$(mktemp -d "$test_root/ii6-live.XXXXXX")"
mkdir -p "$qa/gallery" "$qa/data" "$qa/models"
export GOWORK=off GOTOOLCHAIN=local
go version
python3 -m venv "$qa/venv"
"$qa/venv/bin/python" -m pip install gallery-dl==1.32.19
test "$("$qa/venv/bin/gallery-dl" --version)" = 1.32.19
go build -mod=readonly -o "$qa/monbooru" ./cmd/monbooru
(cd meloader && go build -mod=readonly -o "$qa/monloader" ./cmd/monloader)
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"

cat > "$qa/monbooru.toml" <<EOF
default_gallery = "LiveTest"
setup_done = true

[[galleries]]
name = "LiveTest"
gallery_path = "$qa/gallery"

[server]
bind_address = "127.0.0.1:$port"

[paths]
data_path = "$qa/data"
model_path = "$qa/models"

[desktop]
managed_acquisition = true
meloader_config = "$qa/monloader.toml"

[log]
level = "info"

[schedule]
mode = "off"
EOF
cat > "$qa/monloader.toml" <<EOF
[gallerydl]
binary_path = "$qa/venv/bin/gallery-dl"
sleep_request = 1.0

[downloader]
max_items_per_job = 3

[tags]
strict_blacklist = true
blacklist = ["violence", "rating:sensitive", "rating:questionable", "rating:explicit"]
EOF

printf 'Profile: %s\nLoader: http://127.0.0.1:%s/loader\n' "$qa" "$port"
"$qa/monbooru" -config "$qa/monbooru.toml"
```

## Retest the owner workflow

1. Open the printed Loader URL. Select **LiveTest**, paste a known permitted public Derpibooru still-image URL, and set **Maximum posts** to 1. Queue it while offline and verify that it remains queued.
2. Select **Go online**. Open the job's post results and then the image in the gallery. Check the source URL, visible tags, and rating. Use the **Unrated** view to inspect a source item with no rating if your test data includes one.
3. Repeat the permitted post. Verify the job completes as a duplicate and the gallery contains one copy. Run a permitted search with **Maximum posts** set to 3. Check that its results contain at most three posts, including blacklist exclusions, and use the selected gallery. Repeat the original post again after the search.
4. Open **Settings → Acquisition** in the running profile and edit **Strict blacklist**. Submit a known matching post and verify Loader reports **Blacklisted**. Pre-existing media should stay visible. If a transfer stays active long enough, select **Cancel** and then **Retry**. Retry creates a fresh job against the same gallery and uses the current policy.
5. Select **Go offline** and open an imported image again. Press Ctrl-C in the terminal. Rerun only the normal launch line with the same binary and config. Check that offline status, gallery files and queue history persist, then test **Quit Mebooru** from Settings.

Record the URL type, configured limit, status counts and any first failure from Loader or the terminal. Keep the profile folder for diagnosis. The live check is limited to this profile and requires no companion web server or pairing.

## Test the Chapter III watches and PTR work

Use `ac2b217d2cc6c69ed18fc9c6736b12b7f4061d13` in the checkout command and build both executables again. The existing **Settings → Acquisition** controls are included.

- In **Loader → Watches**, add a known public, general-rated still-image URL with a one-post limit and leave it set to **Manual**. Select **Check now** while offline and verify the job queues. After going online, check the result in the default gallery. Set an interval only if you want automatic checks while Mebooru is open and acquisition is online.
- In the disposable acquisition configuration, set `[ptr].enabled = true` and `[ptr].data_path` to a disposable directory. Use a test copy of an existing `ptr.sqlite` index to verify lookup by SHA-256 offline. To sync, set `[ptr].address` to the test repository, go online, and select **Start sync** in **Loader → PTR**. **Stop sync** and going offline both stop synchronization. Cached tags stay available after restart. Contributions remain follow-up work.

## Candidate scope

- The managed entry point supports Derpibooru posts and searches and the qualified Danbooru and e621 post and tag extractor paths. Derpibooru is the owner acceptance target. Authenticated sites, custom extractor options and raw gallery-dl configuration require further integration. A configured installation of gallery-dl must be trusted by the operator. The app pins the selected launcher bytes during startup and verifies version 1.32.19 inside containment before source work.
- Normal staged publication supports the existing guarded still formats (PNG, JPEG, GIF and WebP). Start with still images. Video, complex claims and cross-volume publication remain compatibility work. Select a gallery on the same volume as the managed profile.
- An exact same-source repeat with matching verified media keeps the existing image, tags, rating and source record. Current acquisition rules still apply before transfer. Source refreshes and replacements need their own workflow. Canceling during publication can leave partial results that require reconciliation. Unresolved payloads retain their reservation.
