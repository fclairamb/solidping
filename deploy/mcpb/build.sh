#!/usr/bin/env bash
# Build the SolidPing MCPB bundle for a released version.
#
#   deploy/mcpb/build.sh v0.36.1     # writes dist/solidping-0.36.1.mcpb
#
# The bundle carries only a small Node launcher. It pins the release tag and the
# SHA-256 of every sp asset (read from the release's sp-checksums.txt), downloads
# the matching binary on first start and refuses one that does not match.

set -euo pipefail

usage() {
  echo "usage: $0 <tag, e.g. v0.36.1>" >&2
  exit 2
}

[[ $# -eq 1 && "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || usage

tag="$1"
version="${tag#v}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/server" "$here/dist"
cp "$here/manifest.json" "$here/icon.png" "$work/"
cp "$here/server/index.js" "$work/server/"

curl -fsSL "https://github.com/fclairamb/solidping/releases/download/${tag}/sp-checksums.txt" \
  -o "$work/sums.txt"

python3 - "$tag" "$version" "$work" <<'PY'
import json, sys
tag, version, work = sys.argv[1:4]
assets = {}
for line in open(f"{work}/sums.txt"):
    parts = line.split()
    if len(parts) == 2 and parts[1].startswith("sp-"):
        assets[parts[1]] = parts[0]
if not assets:
    sys.exit("no sp assets in sp-checksums.txt")
json.dump({"tag": tag, "assets": assets}, open(f"{work}/server/checksums.json", "w"), indent=2)
manifest = json.load(open(f"{work}/manifest.json"))
manifest["version"] = version
json.dump(manifest, open(f"{work}/manifest.json", "w"), indent=2)
PY

rm -f "$work/sums.txt"
out="$here/dist/solidping-${version}.mcpb"
rm -f "$out"
(cd "$work" && zip -q -r "$out" .)
echo "$out"
