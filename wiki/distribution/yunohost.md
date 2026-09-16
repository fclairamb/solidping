# YunoHost package (`solidping_ynh`)

YunoHost does not install Docker images. An app is a packaging repository named
`<app>_ynh` holding a `manifest.toml` (packaging format 2), `scripts/` (install,
upgrade, remove, backup, restore), `conf/` templates (nginx, systemd), `doc/` and
`tests.toml`. So, unlike the [CasaOS listing](casaos.md), this package is **not**
developed in the SolidPing repo — it lives in its own repository.

**Status: the package does not exist yet.** This page is the groundwork: where it will
live, who maintains it, and the one upstream-repo change that silently breaks it. The
user-facing page, [`web/docs/docs/installation/yunohost.md`](../../web/docs/docs/installation/yunohost.md),
says as much and points YunoHost users at the Linux-binary instructions in the meantime.

The design constraints the package has to respect (full domain only, own
authentication, the seeded admin and its forced password rotation, `CAP_NET_RAW` for
ICMP, no browser checks) are written up in the spec that created this page —
`2026-09-16-04-yunohost-native-package.md` — which also carries the draft
`manifest.toml`, systemd unit and nginx block.

## Where it will live

- Package repo: `fclairamb/solidping_ynh` (**not created yet**).
- Catalog entry: a PR to [`YunoHost/apps`](https://github.com/YunoHost/apps) adding a
  `[solidping]` block to `apps.toml` (`state`, `url`, `category` from
  `categories.toml`, optional `subtags` / `potential_alternative_to`), plus a square
  `logos/solidping.png`.
- Before that PR, the package has to pass YunoHost's `package_check` LXC/incus harness.

The YunoHost project strongly encourages transferring app repositories to the
[`YunoHost-Apps`](https://github.com/YunoHost-Apps) organisation. Offering the transfer
in the catalog PR is the expected move — it is what keeps a package maintained if the
original author steps away.

## Catalog quality level

YunoHost computes an app's **level** (0–9) weekly from its own CI, from
`package_check` results plus catalog metadata. The target at submission time is level 6
or above; below that the app shows as low quality in the catalog and the
`package-not-maintained` antifeature appears if updates stop landing.

The app holds **no level today** — it is not in the catalog. Record the level here once
the first CI run produces one, and update it when it moves.

## Autoupdate PRs — who merges them

The manifest uses `autoupdate.strategy = "latest_github_release"`, so YunoHost's
autoupdate bot opens a PR against `solidping_ynh` after each SolidPing GitHub release:
it bumps the version, rewrites the asset URLs and recomputes the sha256 sums.

**Florent reviews and merges those PRs himself** — a few minutes per SolidPing release.
That was the condition for going ahead with the catalog submission at all (see the spec's
resolved open questions); a package whose autoupdate PRs nobody merges earns the
`package-not-maintained` antifeature and is worse than no package.

## The rule that breaks the package silently

The bot matches release assets by regex:

```toml
autoupdate.asset.amd64 = "solidping-linux-amd64$"
autoupdate.asset.arm64 = "solidping-linux-arm64$"
```

**A SolidPing release that renames the Linux release assets breaks that matching.** The
bot does not fail loudly — it just stops producing a usable PR, and the package quietly
falls behind. Any change to the asset names produced by the `server-release` job in
`.github/workflows/ci.yml` (the `OUT` map that names each published binary) must be
followed by a manual bump of `solidping_ynh`:
fix the two `autoupdate.asset.*` patterns and the `[resources.sources.main]` URLs in the
same change.

The same applies if a release ever stops publishing `solidping-linux-arm64`: the manifest
declares both architectures, and an arm64-less release makes the package uninstallable on
the Raspberry Pi hardware that is a good part of YunoHost's install base.

There is no repo-wide release checklist listing every downstream pin (the CasaOS entry,
the fleet image pins and now this package are each documented at their own point of use).
If one gets created, add this to it.
