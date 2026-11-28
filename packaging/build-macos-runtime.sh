#!/bin/sh
set -eu

bundle=${1:?Pass the app Contents directory}
stage=${2:?Pass the package staging directory}
asset=cpython-3.12.14+20260901-aarch64-apple-darwin-install_only_stripped.tar.gz
python_sha=81a359f1cfadd4da11766534c5913791cea55f26e1bb902cacd2a531bb1e4b2b
archive=${MEBOORU_MACOS_PYTHON_ARCHIVE:-"$stage/$asset"}
if [ -z "${MEBOORU_MACOS_PYTHON_ARCHIVE:-}" ]; then
  curl -fsSL -o "$archive" "https://github.com/astral-sh/python-build-standalone/releases/download/20260901/$asset"
fi
test "$(shasum -a 256 < "$archive" | cut -d ' ' -f 1)" = "$python_sha" ||
  { echo 'Bundled Python archive checksum mismatch.' >&2; exit 1; }

runtime="$bundle/Resources/runtime"
mkdir "$runtime"
tar -xzf "$archive" -C "$runtime"
python="$runtime/python/bin/python3"
"$python" -I -m pip --isolated --disable-pip-version-check --no-cache-dir install \
  --index-url https://pypi.org/simple --only-binary=:all: --platform any --require-hashes --no-deps \
  --target "$runtime/packages" -r packaging/macos-runtime-requirements.txt
install -m 0755 packaging/macos-gallery-dl "$runtime/gallery-dl"
install -m 0644 packaging/macos-gallery-dl.py "$runtime/gallery-dl.py"
printf '%s\n' 'Python build source: https://github.com/astral-sh/python-build-standalone/releases/tag/20260901' \
  'gallery-dl source: https://codeberg.org/mikf/gallery-dl/releases/tag/v1.32.19' > "$runtime/sources.txt"
