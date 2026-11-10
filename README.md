<div align="center">

<img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/logo_256.png" alt="SolidPing" width="120">

# SolidPing

**Distributed, self-hostable uptime monitoring.**
44 check types, multi-region workers, private agents, status pages,
incidents and on-call escalation, in a single Go binary.

https://github.com/user-attachments/assets/fe207622-7957-4e85-8a3c-d33281799f4b

<sub>From <code>docker run</code> to the first result in 41 seconds, recorded from a real server with <code>make showcase</code>. <a href="https://solidping.io/docs/tour">The Tour page</a> has the same cut if the player does not load.</sub>

[![Build](https://img.shields.io/github/actions/workflow/status/fclairamb/solidping/ci.yml?branch=main&label=build&logo=github)](https://github.com/fclairamb/solidping/actions/workflows/ci.yml)
[![Coverage](https://codecov.io/gh/fclairamb/solidping/graph/badge.svg)](https://codecov.io/gh/fclairamb/solidping)
[![Release](https://img.shields.io/github/v/release/fclairamb/solidping?label=release&logo=github)](https://github.com/fclairamb/solidping/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/fclairamb/solidping?filename=server%2Fgo.mod&logo=go)](https://github.com/fclairamb/solidping/blob/main/server/go.mod)
[![Go Reference](https://pkg.go.dev/badge/github.com/fclairamb/solidping/server.svg)](https://pkg.go.dev/github.com/fclairamb/solidping/server)
[![License](https://img.shields.io/github/license/fclairamb/solidping?color=blue)](LICENSE)

[![Live](https://img.shields.io/badge/live-www.solidping.io-2ea44f?logo=icloud&logoColor=white)](https://www.solidping.io)
[![Status](https://img.shields.io/badge/status-status.solidping.io-2ea44f?logo=statuspage&logoColor=white)](https://status.solidping.io)
[![Docs](https://img.shields.io/badge/docs-solidping.io%2Fdocs-informational?logo=readthedocs&logoColor=white)](https://solidping.io/docs/)
[![Container](https://img.shields.io/badge/ghcr.io-fclairamb%2Fsolidping-blue?logo=docker&logoColor=white)](https://github.com/fclairamb/solidping/pkgs/container/solidping)

</div>

## Run it

```bash
docker run -p 4000:4000 --hostname solidping -v solidping-data:/data ghcr.io/fclairamb/solidping
```

Open http://localhost:4000 and sign in with `admin@solidping.io` / `solidpass`.
You must pick a new password on first login: the pair is public, so the account can do nothing else until then.

- `--hostname` is required: the worker name comes from it and must match `^[a-z][a-z0-9-]{2,20}$` (or set `SP_NODE_NAME`).
- Data (SQLite database and uploads) lives in `/data`, so keep the volume.
- For production, use PostgreSQL: add `-e SP_DB_TYPE=postgres -e SP_DB_URL='postgresql://user:pass@host:5432/solidping'`. Migrations run on first boot.

Every other setting is an `SP_` environment variable, all optional: see the [configuration docs](https://solidping.io/docs/configuration/).

Other ways to run it:

| | |
|---|---|
| Hosted | [www.solidping.io](https://www.solidping.io), no card needed |
| Docker Compose | [guide](https://solidping.io/docs/installation/docker-compose) |
| Binary (Linux, macOS, Windows) | [releases](https://github.com/fclairamb/solidping/releases/latest), [Linux guide](https://solidping.io/docs/installation/linux), [macOS guide](https://solidping.io/docs/installation/macos), [Windows guide](https://solidping.io/docs/installation/windows) |
| Homebrew | `brew install fclairamb/tap/solidping` |
| winget | `winget install fclairamb.SolidPing` |
| Scoop | `scoop bucket add fclairamb https://github.com/fclairamb/scoop-bucket` then `scoop install solidping` |
| Kubernetes | [Helm chart](deploy/helm/solidping), [guide](https://solidping.io/docs/installation/kubernetes) |
| Railway, Fly.io, Coolify, CasaOS, YunoHost | [installation guides](https://solidping.io/docs/installation/railway) |

## Screenshots

| Checks list | New-check form | Check detail |
|:---:|:---:|:---:|
| <img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/screenshots/checks-list.png" alt="Checks list in the SolidPing dashboard" width="270"> | <img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/screenshots/check-form.png" alt="New-check form, filled in" width="270"> | <img src="https://raw.githubusercontent.com/fclairamb/solidping/main/res/screenshots/check-detail.png" alt="Check detail page with response-time history" width="270"> |

## Check types

| Check | Type | What it checks |
|---|---|---|
| [HTTP/HTTPS](https://solidping.io/docs/features/check-types#httphttps) | `http` | Status codes, body, JSON and header assertions, timing breakdown |
| [TCP](https://solidping.io/docs/features/check-types#tcp) | `tcp` | Port connectivity, optional send/expect payload |
| [UDP](https://solidping.io/docs/features/check-types#udp) | `udp` | Port reachability, optional send/expect payload |
| [ICMP](https://solidping.io/docs/features/check-types#icmp-ping) | `icmp` | Ping |
| [DNS](https://solidping.io/docs/features/check-types#dns) | `dns` | Record resolution |
| [WebSocket](https://solidping.io/docs/features/check-types#websocket) | `websocket` | Connection handshake |
| [RDP](https://solidping.io/docs/features/check-types#rdp-remote-desktop) | `rdp` | RDP negotiation, optional authenticated logon |
| [VNC](https://solidping.io/docs/features/check-types#vnc) | `vnc` | RFB handshake and authentication |
| [Website crawl](https://solidping.io/docs/features/check-types#website-crawl) | `crawl` | Broken links, mixed content, sitemap errors |
| [Application health](https://solidping.io/docs/features/check-types#application-health) | `health` | Health endpoint, reported per component |
| [Vendor status page](https://solidping.io/docs/features/check-types#vendor-status) | `vendor-status` | Outages reported by a vendor's status page |
| [SSL/TLS](https://solidping.io/docs/features/check-types#ssltls-certificate) | `ssl` | Certificate validity and expiration |
| [Domain](https://solidping.io/docs/features/check-types#domain-expiration) | `domain` | Domain expiration (WHOIS/RDAP) |
| [DNSBL](https://solidping.io/docs/features/check-types#dnsbl-dns-blocklist) | `dnsbl` | DNS blocklist membership |
| [PostgreSQL](https://solidping.io/docs/features/check-types#postgresql) | `postgresql` | Connection and query |
| [MySQL/MariaDB](https://solidping.io/docs/features/check-types#mysql--mariadb) | `mysql` | Connection and query |
| [MongoDB](https://solidping.io/docs/features/check-types#mongodb) | `mongodb` | Ping command |
| [Redis](https://solidping.io/docs/features/check-types#redis) | `redis` | PING command |
| [MSSQL](https://solidping.io/docs/features/check-types#microsoft-sql-server) | `mssql` | Connection and query |
| [Oracle](https://solidping.io/docs/features/check-types#oracle-database) | `oracle` | Connection and query |
| [ClickHouse](https://solidping.io/docs/features/check-types#clickhouse) | `clickhouse` | Connection and query |
| [SMTP](https://solidping.io/docs/features/check-types#smtp) | `smtp` | Connectivity, STARTTLS, AUTH, optional real probe email |
| [IMAP](https://solidping.io/docs/features/check-types#imap) | `imap` | Server availability |
| [POP3](https://solidping.io/docs/features/check-types#pop3) | `pop3` | Server availability |
| [Email reception](https://solidping.io/docs/features/check-types#email-reception-passive-inbox) | `email` | A known message reaches an inbox (JMAP) |
| [SSH](https://solidping.io/docs/features/check-types#ssh) | `ssh` | Server availability |
| [FTP](https://solidping.io/docs/features/check-types#ftp) | `ftp` | Server availability |
| [SFTP](https://solidping.io/docs/features/check-types#sftp) | `sftp` | Server availability |
| [gRPC](https://solidping.io/docs/features/check-types#grpc) | `grpc` | Service health |
| [Kafka](https://solidping.io/docs/features/check-types#kafka) | `kafka` | Broker connectivity |
| [RabbitMQ](https://solidping.io/docs/features/check-types#rabbitmq) | `rabbitmq` | Broker connectivity |
| [MQTT](https://solidping.io/docs/features/check-types#mqtt) | `mqtt` | Broker connectivity |
| [SIP](https://solidping.io/docs/features/check-types#sip-voip) | `sip` | VoIP server OPTIONS ping |
| [NTP](https://solidping.io/docs/features/check-types#ntp-time-server) | `ntp` | Reachability and clock drift |
| [SNMP](https://solidping.io/docs/features/check-types#snmp) | `snmp` | Device monitoring |
| [Docker](https://solidping.io/docs/features/check-types#docker) | `docker` | Container health |
| [Kubernetes](https://solidping.io/docs/features/check-types#kubernetes-workload-replica-health) | `kubernetes` | Workload replica health |
| [A2S](https://solidping.io/docs/features/check-types#a2s-game-server-source--steam) | `a2s` | Source / Steam game server query |
| [Minecraft](https://solidping.io/docs/features/check-types#minecraft) | `minecraft` | Java and Bedrock server query |
| [Freebox line](https://solidping.io/docs/features/check-types#freebox-line-xdsl--ftth) | `freebox_line` | xDSL/FTTH line quality |
| [Prometheus](https://solidping.io/docs/features/check-types#prometheus-metric) | `prometheus` | Thresholds on a scraped metric |
| [Heartbeat](https://solidping.io/docs/features/check-types#heartbeat) | `heartbeat` | Passive: alerts when pings stop (cron jobs, CI) |
| [JavaScript](https://solidping.io/docs/features/javascript-checks) | `js` | Sandboxed scripts: multi-step API flows, logins, aggregation |
| [Browser](https://solidping.io/docs/features/check-types#browser) | `browser` | Headless Chrome: full render, screenshots |

## Inspiration

- [Uptime Kuma](https://github.com/louislam/uptime-kuma): a self-hosted monitor that is easy to run ([migration guide](https://solidping.io/docs/features/migrate/from-uptime-kuma)).
- [Better Stack](https://betterstack.com): incidents, on-call escalation and status pages in one product ([migration guide](https://solidping.io/docs/features/migrate/from-better-stack)).
- [Gatus](https://github.com/TwiN/gatus): checks defined as configuration ([migration guide](https://solidping.io/docs/features/migrate/from-gatus)).
- [UptimeRobot](https://uptimerobot.com): fast onboarding, one URL and you are monitoring ([migration guide](https://solidping.io/docs/features/migrate/from-uptimerobot)).
- [Healthchecks.io](https://healthchecks.io): heartbeat monitoring for cron jobs.
- [Checkly](https://www.checklyhq.com): scripted API and browser checks.

## Contributing

Bug reports and pull requests are welcome. [`CONTRIBUTING.md`](CONTRIBUTING.md) covers local setup (`make dev`)
and PR conventions. Found a security issue? See [`SECURITY.md`](SECURITY.md) rather than opening a public issue.

## License

AGPL-3.0 - See [LICENSE](LICENSE).
