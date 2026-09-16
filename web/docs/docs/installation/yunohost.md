---
sidebar_position: 8
title: YunoHost
---

# YunoHost Installation

[YunoHost](https://yunohost.org/) is a self-hosting distribution that installs apps from
its own catalog, as native packages rather than Docker images. SolidPing is a single
static Go binary with SQLite storage, so it fits that model: the package downloads the
release binary, runs it under systemd, and puts nginx in front of it.

:::info Not in the catalog yet
The `solidping_ynh` package is still being written. Once it is published to the YunoHost
catalog the command below works as shown. Until then, install SolidPing on a YunoHost
server the same way as on any other Debian machine — see
[Linux Binary](./linux.md), which covers the systemd unit and the nginx reverse proxy.
:::

## Install from the catalog

```bash
yunohost app install solidping
```

The installer asks for a domain, then sets up the binary, a dedicated system user, a
systemd service, the nginx configuration and a Let's Encrypt certificate.

## SolidPing needs a full domain

SolidPing serves the dashboard, status pages, documentation and API at fixed prefixes
(`/d`, `/s`, `/docs`, `/api`). There is no base-path setting, so it cannot be installed
under a subpath: `example.org/solidping` does not work. Give it a domain or a subdomain
of its own, for example `status.example.org`, and add that domain in YunoHost before
installing.

The app also brings its own accounts. It does not use YunoHost's LDAP directory or the
SSO login — you sign in with a SolidPing account, and public status pages under `/s`
stay reachable without any login.

## First login

Open `https://<your-domain>/` once the install finishes.

**Default credentials:**
- Email: `admin@solidping.io`
- Password: `solidpass`

:::warning First login sets a new password
That password is published in the SolidPing repository, so it buys you exactly one
login. SolidPing lands you on a "set a new password" screen and the account can do
nothing else until you complete it.
:::

## Limitations

- **Browser checks are not available.** [Browser checks](../features/check-types.md#browser)
  drive a headless Chrome over CDP, and the package installs no browser. Everything else
  — HTTP, TCP, DNS, ICMP, SSH, TLS certificates and the rest — works.
- **No subpath install.** Full domain only, for the reason above.

ICMP checks do work: the systemd unit grants the service `CAP_NET_RAW`, so pings are
sent from the YunoHost server itself with no extra configuration.

## Email alerts

Alert emails go out through the SMTP server you configure in SolidPing. On a YunoHost
server the local Postfix relay on `127.0.0.1:25` is usually the right answer — see the
`email` block in the [configuration reference](../configuration/index.md).
