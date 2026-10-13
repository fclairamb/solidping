# Operating a deployed instance: gotchas

Behaviours of the server that surprised whoever ran a real deployment. Each one
looked like a different problem at first. Deployment-specific runbooks (hosts,
clusters, secrets) live in the operator's private infrastructure repo, not here.

## Know what you are talking to

- `GET /api/mgmt/version` first. It gives the version and commit (is this the build you
  think?) and the run mode (which credentials apply). When a host can be routed either
  to a cluster or to a developer laptop, the version string tells you which one
  answered.
- Check the deployed version against the migration that ships a column before planning
  a diagnosis around that column. A diagnostic field added in release N does not exist
  on an instance running N-1.

## Migrations and rolling a release

- Every pod that opens the main database runs migrations at boot, whatever its
  `SP_NODE_ROLE`, and the migrator takes no lock. Roll the API first and let it
  finish, then the checks workers. On one fleet bump the API beat a worker by 818 ms:
  luck, not design. Deported agents never migrate.
- A migration is recorded as applied only after it succeeded (spec 2026-10-13-01). One that
  fails or is killed half way is retried on the next boot, so statements that already ran can
  answer `already exists`: that is a loud startup error naming the migration, fix the migration
  or the data. SQLite also builds a new database under `solidping.db.init` and renames it once
  fully migrated (a leftover `.init` file is deleted at the next start), and copies an existing
  database to `solidping.db.pre-<migration>.bak` before pending migrations (latest only; skipped
  when free disk space is under twice the database size).
- **Half-migrated database.** Versions up to 0.38.3 recorded a migration before running it, so a
  first start stopped early (or a failing migration) left a database that claims a schema it
  lacks (`no such column: organization.default_escalation_policy_uid`). The startup error now
  says so. Nothing repairs it automatically: restore a `.pre-*.bak` if there is one, or, on a
  new install with no data, delete the database file and start again. `solidping migrate repair`
  only re-records checksums, it does not run missing DDL.
- Old workers break on a new schema or a new job type (`column ... does not exist`,
  `unknown job type: ...`) and crash-loop. When `git diff vA..vB -- server/internal/db/*/migrations`
  is non-empty, bump every worker right after the API. A worker must never be ahead of
  its API: it would run the migration under the old API.
- Bump every deployment of one environment to the same image tag. Never deploy
  `:latest` (it drifted once with nothing forcing a re-pull).

## Configuration that silently does nothing

- The koanf env loader turns every `_` after `SP_` into a `.`, so a multi-word key
  (`rate_limiting`, `requests_per_minute`, `trusted_proxies`, `shutdown_timeout`) is
  only read through a manual reader in `server/internal/config/config.go`
  (`applyRateLimitingEnv` and friends). Those readers want the exact full name: the
  deployment that set `SP_RATE_LIMITING_TRUSTED_PROXIES` (no `SERVER_`) had no trusted
  proxies and bucketed every client under the ingress IP. Verify live:
  `GET /api/mgmt/limits` returns `callerIp`; an internal cluster address there means
  trusted proxies are not applied.
- `SP_DEPLOYMENT_MODE` must be identical on API and checks pods. Checks pods enforce
  `maxChecksPerMinute` at dispatch, its default comes from the deployment mode, and the
  `self-hosted` default is unlimited. A checks pod missing `saas` runs uncapped, which
  also uncaps SMS and voice paging spend. The reservation itself fails open
  (`ReserveCheckExecution failed; running check anyway`).
- Under `hostNetwork`, `os.Hostname()` returns the node FQDN, which fails the worker
  slug rule. Set `SP_NODE_NAME`. Without a stable name, every restart registers a new
  `workers` row, and nothing garbage-collects in-process worker rows (the agent GC only
  retires deported agents). Under `hostNetwork`, Kubernetes also defaults `hostPort` to
  `containerPort`, so a checks pod that inherits `containerPort: 4000` reserves the
  node's `:4000`; set `ports: null`.
- A region whose workers report unknown (NULL) capabilities is running an image older
  than the egress self-probe. NULL means "nothing probed", deliberately distinct from
  "none". Check the image tag before the network.
- An integration is only `Active()` when its token and id are non-empty. An expired
  token still flips the dashboard to "enabled" while every send fails. Leave the
  setting empty rather than filling it with a dead value.
- Set `SP_TELEGRAM_BOT_USERNAME` explicitly. The connect surface needs it, and a
  boot-time `getMe` call can lose to a cold DNS cache.

## One callback URL per provider app

Telegram bots, Discord applications and Meta apps each hold one webhook or
interactions URL. Two environments sharing one app steal each other's callbacks.
Use one bot or app per environment (WhatsApp can instead use one app with a
per-phone-number webhook override). For Discord, set `SP_DISCORD_PUBLIC_KEY` and the
bot token and restart before saving the Interactions Endpoint URL: Discord probes it
with a valid and an invalid signed ping and deactivates it if verification fails. An
unsigned `POST` there answering 401 is correct. Mention commands and thread-reply
ingestion also need `SP_DISCORD_GATEWAY_ENABLED` (off by default).

## Signed service calls and renamed orgs

`X-SP-Signature` covers the request path. A call addressed to an org's previous slug
gets redirected (301/308) by `orgSlugRedirect`, the client follows it with the original
signature, and verification fails as a generic 401 (`signature mismatch` in the server
log). When a signed push 401s, check the path first:
`curl -o /dev/null -w '%{http_code} %{redirect_url}' <url>`. A redirect means a stale
slug on the caller's side, not a wrong key.

## Checks that look healthy or broken for the wrong reason

- `status: validating` means failures were observed and the threshold is not crossed
  yet. It is a failing state, not a warm-up.
- `POST /orgs/:org/checks/validate` validates the config shape only; it never connects.
  The real driver error is in a result's `output.error`
  (`GET /orgs/:org/results?checkUid=<uid>`). A failure in ~15-30 ms is a protocol-level
  rejection (auth); a network problem sits at the timeout.
- A port closed from a public region may be open from inside the target network. Probe
  from a private location in that network before calling it down. A name that resolves
  can be a DNS wildcard hit; check the answer.
- A passive email check only reads the ingest mailbox's Inbox. Mail the receiving
  server classifies as spam lands in Junk and the check sees "no email in period", with
  no error. List the Junk, Inbox and Processed folders first; their dates say whether
  mail stopped, got junked, or the poller died. A manual test mail from a large
  provider (aligned DKIM) can land in Inbox and make the check flap up while the real
  sender still gets junked. Behind a Kubernetes LoadBalancer that SNATs (the default
  `externalTrafficPolicy: Cluster`), the mail server sees every client as a cluster IP
  and SPF always fails; `externalTrafficPolicy: Local` keeps the client IP.
- A maintenance window with no attached checks or groups suppresses nothing (see
  [notifications](../features/notifications-and-escalation.md#maintenance-windows)).

## Realtime updates stop with no error

Signature: the WebSocket handshake and `subscribed` acks work, but no `update` frame
ever arrives. Cause seen once: a listener leak in the Postgres event notifier backed up
the single `pq.Listener` queue for hours (fixed since with `Unlisten`). Fast tells:

- `SELECT pg_notification_queue_usage();` visibly above 0 means a listener is behind.
- `/metrics`: `solidping_realtime_hints_published_total` far above
  `..._delivered_total`.
- A goroutine dump with hundreds of goroutines blocked on `pq.(*Listener).Ping`.

The image is distroless (no shell, no pprof by default). Get a goroutine dump with
`kubectl debug <pod> --image=busybox:1.36 --target=backend -q -- sh -c 'kill -QUIT 1'`,
then `kubectl logs <pod> --previous`. The pod restarts, which also clears the backlog.

## Database connections

- Pools of every pod sharing one Postgres role must sum below the role's connection
  limit (`SP_DB_MAX_OPEN_CONNS` / `SP_DB_MAX_IDLE_CONNS` per deployment). The overrun
  only fires on a cold start, when all check runners reserve a connection at once:
  `too many connections (SQLSTATE=53300)` confined to the minute after boot. Look at
  the per-minute distribution before calling it a steady stream.
- Transaction-mode PgBouncer is not a drop-in: the jobs channel uses `LISTEN`/`NOTIFY`.
- A busy server logs about a dozen lines per second, so the container log rotates
  within a minute. A clean `kubectl logs` window is not proof of recovery; read the
  log store.
