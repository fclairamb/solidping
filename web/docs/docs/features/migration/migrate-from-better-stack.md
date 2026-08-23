---
sidebar_position: 2
slug: /features/migrate-from-better-stack
title: Migrate from Better Stack
description: Import your Better Stack monitors and heartbeats into SolidPing with one API token.
---

# Migrate from Better Stack

SolidPing reads your [Better Stack](https://betterstack.com/uptime) account through
its public API and converts every monitor **and** every heartbeat into a SolidPing
check. You paste one API token; we do the paging.

## Where to find your API token

1. Sign in to Better Stack and open **Uptime**.
2. Go to **Settings → API tokens** (`https://uptime.betterstack.com/team/…/api-tokens`).
3. Create a token — read access is enough — and copy it.

:::info The token is never stored
The token is sent once, used as a bearer credential to read
`/api/v2/monitors` and `/api/v2/heartbeats`, and then discarded. SolidPing never
writes it to the database, never logs it, and never echoes it back in an error
message. Revoke it in Better Stack once the migration is done.
:::

## Import it

1. Open **Checks** in the dashboard and click **Import**.
2. Pick **Better Stack (API token)** as the source.
3. Paste the token and click **Import preview** — nothing is written yet.
4. Review what would be created and the list of items that did not map exactly.
5. Confirm.

Via the API:

```bash
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"token":"YOUR_BETTER_STACK_TOKEN"}' \
  'https://your-instance/api/v1/orgs/myorg/checks/import/convert?source=betterstack&dryRun=true' | jq '.'
```

Drop `&dryRun=true` to apply. The endpoint requires an **organization admin** token.

## What maps

| Better Stack `monitor_type` | SolidPing |
|---|---|
| `status`, `expected_status_code` | `http` |
| `keyword` | `http`, body must contain the keyword |
| `keyword_absence` | `http`, body must **not** contain the keyword |
| `ping` | `icmp` |
| `tcp` | `tcp` |
| `udp` | `udp` |
| `smtp` | `smtp` |
| `pop` | `pop3` |
| `imap` | `imap` |
| `dns` | `dns` |

| Better Stack field | SolidPing |
|---|---|
| `pronounceable_name` | check name (the slug is derived from it) |
| `check_frequency` | check period |
| `request_timeout` | checker timeout |
| `http_method`, `request_body` | HTTP method and body |
| `request_headers` | HTTP headers — credential-looking ones (`Authorization`, `X-Api-Key`, …) land in the **encrypted** secret-headers field |
| `required_keyword` | body must / must not contain |
| `expected_status_codes` | expected status codes |
| `paused` | the check is imported **disabled** |
| `ip_version` | `ipVersion` — but see the warning below |

:::warning `ip_version` does not mean the same thing on both sides
In Better Stack an **unset** `ip_version` means *monitor over both IPv4 and
IPv6*. In SolidPing a check probes **one** family — `auto` means "pick one",
exactly as it always has (see
[IP version](/docs/features/check-types#ip-version)).

Monitors that pinned `ipv4` or `ipv6` are imported pinned. Monitors that left it
unset are imported as `auto`, and the import preview says how many — because
those are the ones whose coverage silently halves. Create a second check pinned
to `ipv6` for every target where IPv6 reachability matters.

A pinned value on a type SolidPing cannot pin (for example `dns`) is reported as
its own warning rather than dropped quietly.
:::

### Heartbeats

Every heartbeat becomes a SolidPing [heartbeat check](/docs/features/check-types):

| Better Stack | SolidPing |
|---|---|
| `name` | check name |
| `period` | the expected ping interval (check period) |
| `grace` | the incident confirmation period |
| `paused` | the check is imported disabled |

:::warning Repoint your cron jobs
SolidPing issues its **own** ping URLs — the Better Stack heartbeat URLs will not
work. After the import, open each heartbeat check, copy its new ping URL, and
update the cron job or agent that pushes to it.
:::

## What does not map

Reported as warnings on the import preview:

- **`playwright` monitors** — browser journeys are not converted. Recreate them as
  SolidPing browser checks.
- **Basic-auth credentials** (`auth_username` / `auth_password`) — deliberately
  never imported. SolidPing has a field for them, but an import must not
  silently re-persist secrets read out of another provider's account. Re-enter
  them on the check.
- **`verify_ssl: false` and `follow_redirects: false`** — SolidPing verifies
  certificates and follows redirects.
- **Monitor groups** — assign a SolidPing check group manually after the import.
- **`ssl_expiration` / `domain_expiration`** — in SolidPing these are dedicated
  `ssl` and `domain` checks; add one per host.
- **Escalation policies and on-call schedules** — configure SolidPing
  [on-call](/docs/features/on-call) separately.

## After the import

Checks created this way carry the label `solidping.io/managed=betterstack`, so you
can filter on them and re-run the import while you are still cutting over.
