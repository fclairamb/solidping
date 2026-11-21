# Arch Linux AUR package (`solidping-bin`)

Arch (and CachyOS, Manjaro, EndeavourOS) users install SolidPing from the AUR as
`solidping-bin`: the prebuilt `solidping-linux-{amd64,arm64,riscv64}.gz` release
asset, plus a systemd unit and a sysusers entry. Requested in issue #945. The package
is developed in this repo, at [`deploy/aur/`](../../deploy/aur/), and the AUR git
repository only receives the rendered files.

**Status:** packaging and CI ready; **not on the AUR yet**. The first publication
needs an operator action (below).

## Files

| File | Role |
|---|---|
| `deploy/aur/update.sh <version> [checksums] [pkgrel]` | Renders `PKGBUILD` and `.SRCINFO` for a release, with the sha256 of each `.gz` taken from `solidping-checksums.txt`. Never edit those two by hand. |
| `deploy/aur/test.sh` | Checks the committed files are what `update.sh` renders, then in an `archlinux:base-devel` container: `makepkg --printsrcinfo` agrees with `.SRCINFO`, the package builds from the real release assets, passes `namcap`, installs, and `solidping --version` runs. Needs docker. |
| `solidping-bin/solidping.service` | The unit: `/usr/bin/solidping serve` as the `solidping` user from `/var/lib/solidping` (`StateDirectory=`), optional `EnvironmentFile=-/var/lib/solidping/config.env`. Same hardening and capabilities as the unit `solidping service install` writes (`server/cmd_service_systemd.go`): keep the two in step. |
| `solidping-bin/solidping.sysusers` | Creates the `solidping` system account at install. |
| `solidping-bin/solidping-bin.install` | Post-install and post-upgrade messages. |

The committed `PKGBUILD` names one release (the latest when it was last rendered).
It doesn't need bumping in the repo: the release job renders the tag's version.

## CI

- `aur-package` (every PR and push, outside the `ci` gate since it depends on the Arch
  mirrors): runs `deploy/aur/test.sh`.
- `aur-publish` (stable tags, after `server-release`): renders the package for the tag,
  then, if the `AUR_SSH_PRIVATE_KEY` secret exists, clones
  `ssh://aur@aur.archlinux.org/solidping-bin.git` and pushes the update. Without the
  secret it logs a notice and succeeds. The AUR host key is pinned in the job.

## Publishing for the first time (operator)

1. Create an account on <https://aur.archlinux.org> and add an SSH public key to it
   (a key dedicated to this CI, e.g. `ssh-keygen -t ed25519 -f aur-solidping`).
2. Store the private key in gopass, then as the repository secret
   `AUR_SSH_PRIVATE_KEY` (`gh secret set AUR_SSH_PRIVATE_KEY < aur-solidping`).
3. Either wait for the next release, or push by hand once: `git clone
   ssh://aur@aur.archlinux.org/solidping-bin.git` (an empty clone of an unknown package
   name creates it on first push), copy the five files of `deploy/aur/solidping-bin/`,
   commit, `git push origin master`.

Once it is live, add an "Arch Linux (AUR)" section to
`web/docs/docs/installation/linux.md` (`yay -S solidping-bin`, then
`sudo systemctl enable --now solidping`).

## What breaks the package

- Renaming the `solidping-linux-*.gz` assets or `solidping-checksums.txt`
  (`server-release` in `.github/workflows/ci.yml`): update the `asset` map in
  `update.sh`. `aur-package` fails on the next PR if the names drift.
- Publishing the binary uncompressed or as a tarball: `package()` relies on makepkg
  decompressing `solidping-<ver>-<arch>.gz` into `solidping-<ver>-<arch>`.
- A `solidping serve` that stops reading `config.yml` and the SQLite database from its
  working directory.
