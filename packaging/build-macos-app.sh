#!/bin/sh
set -eu

# Default to the imported companion. An explicit path still supports rollback builds.
loader_src=${1:-meloader}
loader_src=$(cd "$loader_src" && pwd)
output=${2:-dist}
mkdir -p "$output"
output=$(cd "$output" && pwd)
test "$(go env GOHOSTOS)" = darwin || { echo 'Build the app on macOS.' >&2; exit 1; }

. ./packaging/release-env.sh
arch=$(go env GOHOSTARCH)
app="$output/Mebooru.app"
if [ -e "$app" ]; then
  echo "$app already exists. Choose an empty output directory." >&2
  exit 1
fi
stage=$(mktemp -d "$output/.mebooru-build.XXXXXX")
trap 'rm -rf "$stage"' EXIT
bundle="$stage/Mebooru.app/Contents"
mkdir -p "$bundle/MacOS" "$bundle/Resources"

export GOOS=darwin GOARCH="$arch" CGO_ENABLED=0 GOWORK=off
managed_default=false
if [ "$arch" = arm64 ] || [ -n "${MEBOORU_GALLERY_DL:-}" ]; then
  managed_default=true
fi
go build -trimpath -ldflags="$LDFLAGS -X 'main.defaultDesktop=true' -X 'main.useBundledMeloader=true' -X 'main.defaultManagedAcquisition=$managed_default' -X '$MOD.Package=macos-app'" \
  -o "$bundle/MacOS/monbooru" ./cmd/monbooru
(
  cd "$loader_src"
  . ./packaging/release-env.sh
  go build -trimpath -ldflags="$LDFLAGS -X '$MOD.Package=macos-app'" \
    -o "$bundle/MacOS/monloader" ./cmd/monloader
  cp LICENSE "$bundle/Resources/LICENSE-Meloader"
)
cp LICENSE "$bundle/Resources/LICENSE-Mebooru"

if [ -n "${MEBOORU_GALLERY_DL:-}" ]; then
  . "$loader_src/packaging/pins.env"
  runtime_sha=${MEBOORU_GALLERY_DL_SHA256:?Set the trusted SHA-256 of the gallery-dl executable}
  runtime_license=${MEBOORU_GALLERY_DL_LICENSE:?Set the path to the pinned gallery-dl LICENSE}
  test "$(shasum -a 256 < "$MEBOORU_GALLERY_DL" | cut -d ' ' -f 1)" = "$runtime_sha" ||
    { echo 'gallery-dl executable checksum mismatch.' >&2; exit 1; }
  test "$(shasum -a 256 < "$runtime_license" | cut -d ' ' -f 1)" = "$GDL_LICENSE_SHA256" ||
    { echo 'gallery-dl license checksum mismatch.' >&2; exit 1; }
  install -m 0755 "$MEBOORU_GALLERY_DL" "$bundle/MacOS/gallery-dl"
  install -m 0644 "$runtime_license" "$bundle/Resources/LICENSE-gallery-dl"
  printf 'gallery-dl %s source: https://codeberg.org/mikf/gallery-dl/releases/tag/v%s\n' +    "$GDL_VERSION" "$GDL_VERSION" > "$bundle/Resources/gallery-dl-source.txt"
  codesign --force --sign - "$bundle/MacOS/gallery-dl"
  gallery_dl="$bundle/MacOS/gallery-dl"
elif [ "$arch" = arm64 ]; then
  sh packaging/build-macos-runtime.sh "$bundle" "$stage"
  gallery_dl="$bundle/Resources/runtime/gallery-dl"
fi
if [ "$managed_default" = true ]; then
  . "$loader_src/packaging/pins.env"
  test "$(MEBOORU_RUNTIME_WHEEL= "$gallery_dl" --config-ignore --version)" = "$GDL_VERSION" ||
    { echo "The bundled gallery-dl must run as version $GDL_VERSION." >&2; exit 1; }
fi
if [ "$arch" = arm64 ]; then
  sh packaging/build-macos-media.sh "$bundle" "$stage"
fi

iconset="$stage/Mebooru.iconset"
mkdir "$iconset"
sips --padToHeightWidth 1448 1448 branding/mebooru-mobius-master.png --out "$stage/icon.png" >/dev/null
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$stage/icon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
  twice=$((size * 2))
  sips -z "$twice" "$twice" "$stage/icon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$bundle/Resources/Mebooru.icns"

cat > "$bundle/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>monbooru</string>
  <key>CFBundleIdentifier</key><string>io.github.jwebb7847-ui.mebooru</string>
  <key>CFBundleName</key><string>Mebooru</string>
  <key>CFBundleDisplayName</key><string>Mebooru</string>
  <key>CFBundleIconFile</key><string>Mebooru.icns</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION#v}</string>
  <key>CFBundleVersion</key><string>${VERSION#v}</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>LSUIElement</key><true/>
</dict>
</plist>
EOF
plutil -lint "$bundle/Info.plist"
codesign --force --sign - "$bundle/MacOS/monloader"
codesign --force --sign - "$stage/Mebooru.app"
codesign --verify --deep --strict "$stage/Mebooru.app"
mv "$stage/Mebooru.app" "$app"
ditto -c -k --sequesterRsrc --keepParent "$app" "$output/Mebooru-macos-$arch.zip"
