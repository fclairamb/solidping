---
sidebar_position: 7
title: CasaOS / ZimaOS
---

# CasaOS / ZimaOS Installation

[CasaOS](https://www.casaos.io/) and [ZimaOS](https://www.zimaspace.com/zimaos) are
home-server operating systems built around a one-click app store. SolidPing publishes
`linux/amd64` and `linux/arm64` images, so it runs on the Raspberry Pi and ARM NAS
hardware most CasaOS/ZimaOS boxes use, as well as x86.

## From the store

Once the [upstream listing](https://github.com/IceWhaleTech/CasaOS-AppStore) is merged,
search the CasaOS App Store for **SolidPing** and click Install. CasaOS creates and owns
the app's data directory for you.

## Install a customized app

Until the store listing is merged — or if you want to change a setting before
installing — use CasaOS's **Install a customized app** screen and paste this compose
file:

```yaml
name: solidping
services:
  solidping:
    image: ghcr.io/fclairamb/solidping:latest
    container_name: solidping
    network_mode: bridge
    restart: unless-stopped
    # Runs as root: CasaOS creates /DATA/AppData/<id>/... as root before the
    # container starts, and the image itself has no shell to `chown` it with.
    # The image stays `nonroot` otherwise — this is compose-only.
    user: "0:0"
    cap_add:
      - NET_RAW
    ports:
      - target: 4000
        published: "4000"
        protocol: tcp
    volumes:
      - type: bind
        source: /DATA/AppData/solidping/data
        target: /data
    healthcheck:
      test: ["CMD", "/app/solidping", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
```

Adjust `/DATA/AppData/solidping/data` to wherever CasaOS puts app data on your install
(the store version substitutes `$AppID` automatically; a hand-typed compose needs a real
path). The full file this is generated from lives at
[`deploy/casaos/solidping/docker-compose.yml`](https://github.com/fclairamb/solidping/blob/main/deploy/casaos/solidping/docker-compose.yml)
in the SolidPing repository, alongside the icon and screenshots used in the store
listing.

## First login

Once running, open the app from the CasaOS dashboard (or `http://<your-nas>:4000`
directly).

**Default credentials:**
- Email: `admin@solidping.io`
- Password: `solidpass`

:::warning First login sets a new password
That password is published in the SolidPing repository, so it buys you exactly one
login. SolidPing lands you on a "set a new password" screen and the account can do
nothing else until you complete it.
:::
