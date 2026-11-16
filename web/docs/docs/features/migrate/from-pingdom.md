---
sidebar_position: 5
title: Migrate from Pingdom
description: Import your Pingdom checks into SolidPing with a read-only API token or a pasted JSON export.
---

# Migrate from Pingdom

SolidPing converts your Pingdom checks (API 3.1) into SolidPing checks. Open
**Checks > More actions > Import**, pick **Pingdom**, and either:

- paste a **read-only API token** (My Pingdom > Settings > Pingdom API > Add API
  token, read access). SolidPing reads your checks once and **never stores the
  token**; or
- paste the JSON returned by `GET https://api.pingdom.com/api/3.1/checks?include_tags=true`
  (or an array of `GET /checks/{id}` responses) if you prefer not to share a token.

Review the dry-run preview, then confirm. Re-importing updates the same checks in
place (they carry the `solidping-managed=pingdom` label).

## What is converted

| Pingdom | SolidPing |
|---|---|
| HTTP / HTTPS | `http` check (URL, port, expected and forbidden text, request headers, POST data) |
| HTTP custom | `http` check on the URL, with a warning |
| TCP, UDP | `tcp`, `udp` (send and expect strings) |
| Ping | `icmp` |
| DNS | `dns` (expected IP, nameserver) |
| SMTP, POP3, IMAP | `smtp`, `pop3`, `imap` |
| Interval, paused state, tags | period, disabled state, labels |
| "Alert after n failures" | confirmation period of n intervals |

## What is not converted

- Transaction (TMS) checks are skipped, with a warning.
- Basic-auth credentials are **not** imported (a warning lists them): re-enter them on the check.
- Probe filters, alert contacts, integrations and maintenance windows.
