#!/bin/sh
set -eu

bundle=${1:?Pass the app Contents directory}
stage=${2:?Pass the package staging directory}
ffmpeg_version=9.0.2
webp_version=1.6.0
sources=${MEBOORU_MACOS_MEDIA_ARCHIVES:-"$stage"}

unpack() {
  archive="$sources/$1"
  if [ -z "${MEBOORU_MACOS_MEDIA_ARCHIVES:-}" ]; then
    curl -fsSL -o "$archive" "$2"
  fi
  test "$(shasum -a 256 < "$archive" | cut -d ' ' -f 1)" = "$3" ||
    { echo "Checksum mismatch for $1." >&2; exit 1; }
  tar -xf "$archive" -C "$stage"
}

unpack "ffmpeg-$ffmpeg_version.tar.xz" "https://ffmpeg.org/releases/ffmpeg-$ffmpeg_version.tar.xz" \
  8c3850283eb25fa026482078a04051e0be17347b09ef81a0849bec15a96e002e
webp_url=https://storage.googleapis.com/downloads.webmproject.org/releases/webp
unpack "libwebp-$webp_version.tar.gz" "$webp_url/libwebp-$webp_version.tar.gz" \
  e4ab7009bf0629fd11982d4c2aa83964cf244cffba7347ecd39019a9e38c4564

export MACOSX_DEPLOYMENT_TARGET=13.0
export MEBOORU_WEBP_DIR="$stage/webp"
cpu_count=$(getconf _NPROCESSORS_ONLN)
(
  cd "$stage/libwebp-$webp_version"
  ./configure --prefix="$MEBOORU_WEBP_DIR" --disable-shared --disable-gl --disable-sdl \
    --disable-png --disable-jpeg --disable-tiff --disable-gif
  make -j "$cpu_count"
  make install
)

# Provide the two local WebP entries FFmpeg configure queries.
cat > "$stage/webp-pkg-config" <<'EOF'
#!/bin/sh
set -eu
if [ "$1" = --version ]; then
  echo 1.0
  exit 0
fi
case "$*" in
  *libwebpmux*) libs="-lwebpmux -lwebp -lsharpyuv -lm" ;;
  *libwebp*) libs="-lwebp -lsharpyuv -lm" ;;
  *) exit 1 ;;
esac
case "$1" in
  --exists) ;;
  --cflags) printf '%s\n' "-I$MEBOORU_WEBP_DIR/include" ;;
  --libs) printf '%s\n' "-L$MEBOORU_WEBP_DIR/lib $libs" ;;
  --variable=includedir) printf '%s\n' "$MEBOORU_WEBP_DIR/include" ;;
  *) exit 1 ;;
esac
EOF
chmod 0755 "$stage/webp-pkg-config"
(
  cd "$stage/ffmpeg-$ffmpeg_version"
  ./configure --disable-autodetect --disable-network --disable-doc --disable-debug --disable-ffplay \
    --disable-everything --enable-libwebp --enable-zlib --pkg-config="$stage/webp-pkg-config" \
    --enable-protocol=file,pipe --enable-demuxer=mov,matroska,gif,image2,png_pipe,jpeg_pipe,webp_pipe,av1 \
    --enable-muxer=image2,webp,null,mp4 --enable-decoder=h264,hevc,vp8,vp9,av1,gif,png,mjpeg,webp,mpeg4 \
    --enable-parser=h264,hevc,vp8,vp9,av1 --enable-encoder=mjpeg,png,libwebp,libwebp_anim \
    --enable-filter=scale,format,null,trim,setpts --enable-swscale
  make -j "$cpu_count" ffmpeg ffprobe
  install -m 0755 ffmpeg ffprobe "$bundle/MacOS/"
  install -m 0644 COPYING.LGPLv2.1 "$bundle/Resources/LICENSE-FFmpeg"
)
install -m 0644 "$stage/libwebp-$webp_version/COPYING" "$bundle/Resources/LICENSE-WebP"
install -m 0644 "$stage/libwebp-$webp_version/PATENTS" "$bundle/Resources/PATENTS-WebP"
printf '%s\n' "FFmpeg source: https://ffmpeg.org/releases/ffmpeg-$ffmpeg_version.tar.xz" \
  "WebP source: $webp_url/libwebp-$webp_version.tar.gz" > "$bundle/Resources/media-tool-sources.txt"
codesign --force --sign - "$bundle/MacOS/ffmpeg"
codesign --force --sign - "$bundle/MacOS/ffprobe"
