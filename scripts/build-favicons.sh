#!/usr/bin/env bash
# Generates the favicon PNG set from res/logo.svg; `make sync-brand-assets`
# then copies it into web/dash0/public/assets/ and web/status0/public/assets/.
# Spec 2026-05-09-01-brand-design-tokens-and-logo-pipeline.md.
#
# Requires either rsvg-convert (preferred, sharpest scaling at small
# sizes) or ImageMagick `magick`. Falls back gracefully and prints which
# tool was used.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SVG="$ROOT/res/logo.svg"
SIZES=(32 192 512)
APPLE_SIZE=180

if [[ ! -f "$SVG" ]]; then
    echo "error: $SVG not found" >&2
    exit 1
fi

render() {
    local size="$1"
    local out="$2"
    if command -v rsvg-convert >/dev/null 2>&1; then
        rsvg-convert -w "$size" -h "$size" -o "$out" "$SVG"
    elif command -v magick >/dev/null 2>&1; then
        magick -background none -density 384 "$SVG" -resize "${size}x${size}" "$out"
    elif command -v convert >/dev/null 2>&1; then
        convert -background none -density 384 "$SVG" -resize "${size}x${size}" "$out"
    else
        echo "error: need rsvg-convert, magick, or convert on PATH" >&2
        exit 1
    fi
}

OUTDIR="$ROOT/res/favicons"
mkdir -p "$OUTDIR"

for size in "${SIZES[@]}"; do
    out="$OUTDIR/favicon-${size}.png"
    render "$size" "$out"
    echo "wrote $out"
done

render "$APPLE_SIZE" "$OUTDIR/apple-touch-icon.png"
echo "wrote $OUTDIR/apple-touch-icon.png"

echo "done. run 'make sync-brand-assets' to copy into web/{dash0,status0}/public/assets/"
