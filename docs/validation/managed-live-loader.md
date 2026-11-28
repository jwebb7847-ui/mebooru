# Managed Loader acceptance on Apple Silicon

Use Chapter II candidate `986c331c02ae7fbf395cf5621f363d2e165b0a55`. It includes the repeat-acquisition, shutdown, recovery, and Loader form fixes. Run the copy-and-paste checkout from your Mebooru repository.

```zsh
git fetch origin codex/ii6-owner-acceptance
qa_checkout="$(mktemp -d "${TMPDIR:-/tmp}/mebooru-acceptance.XXXXXX")"
git worktree add --detach "$qa_checkout/source" 986c331c02ae7fbf395cf5621f363d2e165b0a55
cd "$qa_checkout/source"
```

## Recover the preserved LiveTest profile

Restart the Mac before using the recovery command. After reboot, open Terminal and run the checkout commands above. Build both corrected executables into a new directory and reuse the preserved profile and gallery.

```zsh
test_root="$HOME/Downloads/Projects/Coding/Mebooru/testing"
qa="$test_root/ii6-live.jryLAd"
fixed="$(mktemp -d "$test_root/ii6-fixed.XXXXXX")"
export GOWORK=off GOTOOLCHAIN=local
go build -mod=readonly -o "$fixed/monbooru" ./cmd/monbooru &&
  (cd meloader && go build -mod=readonly -o "$fixed/monloader" ./cmd/monloader) &&
  "$fixed/monbooru" -config "$qa/monbooru.toml" -recover-managed-after-reboot &&
  "$fixed/monbooru" -config "$qa/monbooru.toml"
```

Recovery checks that the old ownership record predates the current Mac boot, takes the profile lock and sets acquisition offline. The original receipt, gallery, queue, and staging evidence stay in place. If recovery refuses, keep the files and share its terminal output. Use the printed Loader URL and confirm the original image and job history are present.

## Start with a fresh disposable gallery

As an alternative to recovering the old profile, run the following from the candidate checkout. It builds both executables, installs the pinned runtime and creates an isolated profile that starts offline.

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
max_items_per_job = 5

[tags]
strict_blacklist = true
blacklist = ["violence"]
EOF

printf 'Profile: %s\nLoader: http://127.0.0.1:%s/loader\n' "$qa" "$port"
"$qa/monbooru" -config "$qa/monbooru.toml"
```

## Retest the owner workflow

1. Open the printed Loader URL. Select **LiveTest**, paste a known permitted public Derpibooru still-image URL, and set **Maximum posts** to 1. Queue it while offline and verify that it remains queued.
2. Select **Go online**. Open the job's post results and then the image in the gallery. Check the source URL, visible tags, and rating. Use the **Unrated** view to inspect a source item with no rating if your test data includes one.
3. Repeat the permitted post. Verify the job completes as a duplicate, the gallery contains one copy, and the next submission runs. Then try a permitted search URL with **Maximum posts** set to 3 and check the selected gallery and result count.
4. Edit `[tags].blacklist` in `$qa/monloader.toml` and submit a known matching post. Verify Loader reports **Blacklisted** and pre-existing media stays visible. If a transfer stays active long enough, select **Cancel** and then **Retry**. Retry creates a fresh job against the same gallery and uses the current policy. Keep the log if a job reports reconciliation required.
5. Select **Go offline** and open an imported image again. Press Ctrl-C in the terminal. Rerun only the normal launch line with the same binary and config. Check that offline status, gallery files and queue history persist, then test **Quit Mebooru** from Settings.

Record the URL type, configured limit, status counts and any first failure from Loader or the terminal. Keep the profile folder for diagnosis. The live check is limited to this profile and requires no companion web server or pairing.

## Test the separate Chapter III Settings work

To test the Settings increment, use `16388ca2ebb37dd45bf057b8ca1026a4b50a2554` in the checkout command and build both executables again. Open **Settings → Acquisition**.

- Edit the strict blacklist and check that the next matching job is blacklisted. Saving preserves other TOML settings and rewrites file formatting. The editor keeps rule comments entered as blacklist lines.
- Change the request delay or maximum posts. Save and check for the restart notice. Quit with **Quit Mebooru** at the top of Settings, then launch the same profile again.
- Check that **Plugins** continues to show pairing requests from external plugins. Managed profiles use the native acquisition controls. PTR and watches continue to use their existing standalone paths pending managed integration.

## Candidate scope

- The managed entry point supports Derpibooru posts and searches and the qualified Danbooru and e621 post and tag extractor paths. Derpibooru is the owner acceptance target. Authenticated sites, custom extractor options and raw gallery-dl configuration require further integration. A configured installation of gallery-dl must be trusted by the operator. The app pins the selected launcher bytes during startup and verifies version 1.32.19 inside containment before source work.
- Normal staged publication supports the existing guarded still formats (PNG, JPEG, GIF and WebP). Start with still images. Video, complex claims and cross-volume publication remain compatibility work. Select a gallery on the same volume as the managed profile.
- An exact same-source repeat with matching verified media and source evidence retains the existing image. Source changes and replacements need their own workflow. Canceling an active transfer parks its unpublished staging evidence for reconciliation and frees the worker for a new job. Canceling during publication can leave partial results that require reconciliation. Abandoned payloads retain their reservation.
