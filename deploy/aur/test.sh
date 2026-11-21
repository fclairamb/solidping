#!/usr/bin/env bash
# Checks the solidping-bin AUR package in an Arch Linux container:
#   1. PKGBUILD and .SRCINFO are what update.sh renders (nobody hand-edited
#      one without the other);
#   2. makepkg --printsrcinfo agrees with the committed .SRCINFO;
#   3. the package builds from the real release assets (asset names and
#      checksums), passes namcap, installs, and its binary runs.
#
#   deploy/aur/test.sh [checksums-file]
#
# Needs docker. The checksums file defaults to the one of the release the
# PKGBUILD names, downloaded with gh.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
pkg="$here/solidping-bin"
version="$(sed -n 's/^pkgver=//p' "$pkg/PKGBUILD")"
pkgrel="$(sed -n 's/^pkgrel=//p' "$pkg/PKGBUILD")"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

checksums="${1:-}"
if [[ -z "$checksums" ]]; then
  gh release download "v$version" --repo fclairamb/solidping -p solidping-checksums.txt -D "$work"
  checksums="$work/solidping-checksums.txt"
fi

# 1. Re-render into a copy and compare.
cp -R "$pkg" "$work/render"
cp "$here/update.sh" "$work/update.sh"
mv "$work/render" "$work/solidping-bin"
"$work/update.sh" "$version" "$checksums" "$pkgrel" >/dev/null
for f in PKGBUILD .SRCINFO; do
  if ! diff -u "$pkg/$f" "$work/solidping-bin/$f"; then
    echo "test.sh: $f is not what update.sh renders; run deploy/aur/update.sh $version" >&2
    exit 1
  fi
done

# 2 and 3, in Arch (x86_64: the only architecture of the official image).
# --disable-sandbox: pacman's seccomp sandbox fails under emulation (arm64 hosts).
docker run --rm --platform linux/amd64 -v "$pkg:/src:ro" archlinux:base-devel bash -euo pipefail -c '
  pacman --disable-sandbox -Syu --noconfirm --needed namcap >/dev/null
  useradd -m builder
  cp -R /src /home/builder/pkg
  chown -R builder: /home/builder/pkg
  cd /home/builder/pkg
  su builder -c "makepkg --printsrcinfo" | diff -u .SRCINFO -
  su builder -c "makepkg --noconfirm"
  namcap PKGBUILD
  namcap solidping-bin-*.pkg.tar.zst
  pacman --disable-sandbox -U --noconfirm solidping-bin-*.pkg.tar.zst
  solidping --version
  getent passwd solidping
  test -f /usr/lib/systemd/system/solidping.service
  # Report only: under emulation (arm64 hosts) it calls the binary not executable.
  systemd-analyze verify /usr/lib/systemd/system/solidping.service 2>&1 | grep -v "^$" || true
'
echo "solidping-bin $version-$pkgrel: OK"
