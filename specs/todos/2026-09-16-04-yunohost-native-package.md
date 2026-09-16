---
model: opus
effort: high
---

# YunoHost has no Docker store: package SolidPing natively as `solidping_ynh` (full-domain, SQLite, systemd)

## Problem

YunoHost does not install Docker images. An app is a packaging repository named
`<app>_ynh` holding a `manifest.toml` (packaging format 2), `scripts/install`,
`upgrade`, `remove`, `backup`, `restore`, `conf/` templates (nginx, systemd),
`doc/` and `tests.toml`. It is tested by `package_check`, enters the catalog
through a PR to `YunoHost/apps` adding an `apps.toml` entry (`state`, `url`,
`category` from `categories.toml`, optional `potential_alternative_to`), gets a
quality level computed weekly by their CI, and the project "strongly
encourages" transferring the repo to the `YunoHost-Apps` organisation. "Submit
the Docker image" is not a path that exists.

What makes it feasible anyway: SolidPing is one static Go binary with SQLite by
default, and every release publishes `solidping-linux-amd64`,
`solidping-linux-arm64` and `solidping-checksums.txt`
(`web/docs/docs/installation/linux.md:12-38`, built with `CGO_ENABLED=0` at
`.github/workflows/ci.yml:506`). That is exactly the input `ynh_setup_source`
with `autoupdate.strategy = "latest_github_release"` wants, so upstream releases
can flow into the package through YunoHost's autoupdate bot.

Product constraints the package has to respect:

1. **Full domain only.** The dashboard, status pages, docs and API are served at
   fixed prefixes (`/d`, `/s`, `/docs`, `/api`;
   `server/internal/config/config.go:412-431`) with no base-path setting, so
   `example.org/solidping` cannot work. The manifest asks only for a domain.
2. **Own authentication.** No LDAP, no SSOwat login; status pages under `/s`
   must stay reachable by anonymous visitors, and the dashboard uses Bearer
   tokens, so SSOwat must not inject `Authorization` headers.
3. **First login.** A fresh database seeds `admin@solidping.io` / `solidpass`
   and forces a password change (`CLAUDE.md`, "Default credentials"). No env
   var seeds a different admin (nothing in `job_startup.go` or `config.go`
   reads one), so the package documents the seeded account rather than asking
   for one.
4. **ICMP checks** need `CAP_NET_RAW` or a `ping_group_range` entry
   (`web/docs/docs/features/traceroute-diagnostics.md:99-100`).
5. **Browser checks** need a Chrome reachable over CDP; out of scope for the
   package, documented as a limitation.

## Proposal

### 1. New repository `fclairamb/solidping_ynh`, generated from `YunoHost/example_ynh`

Not in this repo: YunoHost tooling assumes the `<app>_ynh` layout at the repo
root. This repo only gains a docs page and a wiki page (below).

`manifest.toml` (the keys below are the ones the v2 schema accepts; the
GoToSocial package is the closest working reference, a full-domain Go binary):

```toml
packaging_format = 2
id = "solidping"
name = "SolidPing"
description.en = "Uptime monitoring, alerting and status pages"
description.fr = "Supervision de disponibilité, alertes et pages de statut"
version = "0.28.3~ynh1"
maintainers = ["fclairamb"]

[upstream]
license = "AGPL-3.0-only"
website = "https://www.solidping.io"
admindoc = "https://solidping.io/docs"
code = "https://github.com/fclairamb/solidping"

[integration]
yunohost = ">= 12.0"
helpers_version = "2.1"
architectures = ["amd64", "arm64"]
multi_instance = false
ldap = false
sso = false
disk = "100M"
ram.build = "50M"
ram.runtime = "150M"

[install]
[install.domain]
type = "domain"
[install.init_main_permission]
type = "group"
default = "visitors"

[resources]
[resources.sources.main]
amd64.url = "https://github.com/fclairamb/solidping/releases/download/v0.28.3/solidping-linux-amd64"
amd64.sha256 = "<from solidping-checksums.txt>"
arm64.url = "https://github.com/fclairamb/solidping/releases/download/v0.28.3/solidping-linux-arm64"
arm64.sha256 = "<from solidping-checksums.txt>"
format = "whatever"
extract = false
rename = "solidping"
autoupdate.strategy = "latest_github_release"
autoupdate.asset.amd64 = "solidping-linux-amd64$"
autoupdate.asset.arm64 = "solidping-linux-arm64$"

[resources.system_user]
allow_email = true

[resources.install_dir]

[resources.data_dir]

[resources.permissions]
main.url = "/"
main.allowed = "visitors"
main.auth_header = false
main.show_tile = true

[resources.ports]
main.default = 4000
```

Why these values: no `[install.path]` question is how a v2 manifest declares a
full-domain app; `main.allowed = "visitors"` keeps `/s/...` status pages
public and lets SolidPing's own login gate `/d`; `auth_header = false` stops
SSOwat from sending a Basic header that would collide with Bearer auth;
`extract = false` + `rename` because the release asset is a bare binary, not an
archive; `ram.runtime` from the last `make bench-memory` report, not a guess.

`conf/systemd.service`:

```ini
[Service]
User=__APP__
WorkingDirectory=__INSTALL_DIR__
ExecStart=__INSTALL_DIR__/solidping serve
Environment=SP_SERVER_LISTEN=127.0.0.1:__PORT__
Environment=SP_DB_TYPE=sqlite
Environment=SP_DB_DIR=__DATA_DIR__
Environment=SP_FILESTORAGE_LOCAL_ROOT=__DATA_DIR__/files
AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=__DATA_DIR__
```

`SP_AUTH_JWT_SECRET` is deliberately not set: the server generates one and
persists it as a system parameter in the database
(`server/internal/systemconfig/systemconfig.go:1441-1462`), so it travels with
the SQLite file through backup/restore with nothing extra to save as a
setting.

`conf/nginx.conf`: `location / { proxy_pass http://127.0.0.1:__PORT__; }` with
`proxy_http_version 1.1`, the `Upgrade` / `Connection` headers (the dashboard
keeps long-lived connections for live updates; verify whether it is WebSocket
or SSE and set the matching headers plus a long `proxy_read_timeout`), and
`client_max_body_size` large enough for logo uploads.

Scripts: `install` (setup source, systemd unit, nginx, `ynh_systemd_action
--action=start --line_match="listening"` on the log line the server prints at
startup), `upgrade` (same, stop before replacing the binary; SolidPing runs its
own migrations on boot), `remove`, `backup`/`restore` (install dir, data dir,
nginx, systemd). `doc/DESCRIPTION.md`, `doc/POST_INSTALL.md` (the seeded
credentials and the forced rotation, verbatim from `CLAUDE.md`),
`doc/ADMIN.md` (SMTP: point users at the SMTP block of the config,
`config.go:762-767`, and at YunoHost's local relay on `127.0.0.1:25`; browser
checks unsupported; ICMP works via the capability). `tests.toml` covering
install on a full domain, upgrade from the previous package version,
backup/restore, and `change_url` disabled (full domain only, no path change).

### 2. Test with `package_check`, then the catalog PR

Run `package_check` (their LXC/incus harness) until the install, upgrade,
backup/restore tests pass; target level 6 or above. Then the PR to
`YunoHost/apps`:

```toml
[solidping]
state = "working"
url = "https://github.com/fclairamb/solidping_ynh"
category = "system_tools"
subtags = ["monitoring"]
```

(Uptime Kuma is listed under the same category; copy its `subtags` and set
`potential_alternative_to` to the same hosted services it names.) Add a square
`logos/solidping.png` from `res/logo_256.png`. Offer the repo transfer to
`YunoHost-Apps` in the PR description; it is what they ask for and it is what
keeps the package maintained if the author stops.

### 3. In this repo

- `web/docs/docs/installation/yunohost.md` (`sidebar_position: 8`): install
  from the catalog (`yunohost app install solidping`), the full-domain
  requirement stated plainly, the first-login paragraph, the two limitations
  (browser checks, subpath). No competitor content.
- `wiki/distribution/yunohost.md`: where the package lives, how the autoupdate
  bot's PRs get merged, the level the app currently holds, and the rule that a
  release which renames the binary assets breaks `autoupdate.asset` and must
  be followed by a manual package bump.

## Verification

- `package_check` green on amd64 (and arm64 if a runner is available; the CI
  bot will cover it otherwise).
- On a test YunoHost VM: install on a fresh subdomain, log in, rotate the
  password, create an HTTP check and an ICMP check (proves `CAP_NET_RAW`),
  upload an org logo (proves the data dir and nginx body size), run
  `yunohost backup create` / `restore` and confirm the check and the logo are
  back.
- Upgrade path: install the previous package version, upgrade, confirm the
  migration ran and the data is intact.

## Open questions

- Should the package ask for an SMTP relay at install time and write the SMTP
  block, so alert emails work out of the box through YunoHost's Postfix?
  Default: yes if it is three lines in `install`; otherwise document it in
  `doc/ADMIN.md`.
- `show_tile = true` puts a SolidPing tile in the SSO portal that leads to a
  separate login. Acceptable, or hide the tile? Default: show it.
- Is the maintenance load worth it? Every SolidPing release becomes an
  autoupdate PR to review; a package nobody merges gets the
  `package-not-maintained` antifeature. Decide who reviews them before the
  catalog PR, not after.

## Resolved open questions

- **Is the maintenance load worth it? Decide who reviews the autoupdate PRs
  before the catalog PR, not after.** Florent reviews and merges each
  autoupdate PR himself (a few minutes per SolidPing release). Proceed with
  the catalog PR on that basis; document the reviewer in
  `wiki/distribution/yunohost.md` alongside the autoupdate-bot notes from
  section 3.

## Delivery

External repo `fclairamb/solidping_ynh` (new) plus one PR here: branch
`feat/yunohost-docs`, title `docs(install): YunoHost package page and
distribution notes`.
