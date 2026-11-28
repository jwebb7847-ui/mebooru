# Managed Loader acceptance on Apple Silicon

Run this from a checkout of the acceptance candidate. The commands build the normal sibling executables, install the pinned runtime into a fresh virtual environment and write an isolated profile. The profile starts offline. Every application path in the example belongs to its new testing directory.

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

1. Open the printed Loader URL. Select **LiveTest**, paste a public Derpibooru post or search URL, and set **Post limit** to 1–5. Start with a known still image. Queue it while offline and verify that it remains queued.
2. Select **Go online**. Open the job's post results and then the image in the gallery. Check the source URL, visible tags, and rating. Use the **Unrated** view to inspect a source item with no rating if your test data includes one.
3. Repeat the same permitted post. Verify the result is a duplicate, the gallery has one copy, and the existing image still opens. For a known blocked post, check that Loader reports **Blacklisted** and publishes no file. Edit `[tags].blacklist` in `$qa/monloader.toml` to test your actual rules. New jobs read those rules and the optional `profiles/` directory beside the file. Runtime path, pacing and window limit changes take effect on restart.
4. If a transfer stays active long enough, select **Cancel**, then **Retry**. Retry creates a fresh bounded job against the same gallery and current policy. Preserve the log if cancellation or restart reports reconciliation required.
5. Select **Go offline** and open an imported image again. Quit Mebooru with Ctrl-C in the terminal. Re-run the last command with the same config and check that offline status, gallery files and queue history persist.

Record the URL type, configured limit, status counts and any first failure from Loader or the terminal. Keep the profile folder for diagnosis. The live check is limited to this profile and requires no companion web server or pairing.

## Candidate scope

- The managed entry point supports Derpibooru posts and searches and the qualified Danbooru and e621 post and tag extractor paths. Derpibooru is the owner acceptance target. Authenticated sites, custom extractor options and raw gallery-dl configuration require further integration. A configured installation of gallery-dl must be trusted by the operator. The app pins the selected launcher bytes during startup and verifies version 1.32.19 inside containment before source work.
- Normal staged publication supports the existing guarded still formats (PNG, JPEG, GIF and WebP). Start with still images. Video, complex claims and cross-volume publication remain compatibility work. Select a gallery on the same volume as the managed profile.
- An exact same-source repeat with matching verified media and source evidence retains the existing image. Source changes and replacements need their own workflow. Canceling an active transfer parks its unpublished staging evidence for reconciliation and frees the worker for a new job. Canceling during publication can leave partial results that require reconciliation. Abandoned payloads retain their reservation.
