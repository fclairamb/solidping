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

- Transaction (TMS) checks (both "script" and "recording" types) are imported as
  [JavaScript browser checks](../javascript-checks.md): each step becomes a
  line of a generated script. Steps with no equivalent (`wpm_click_xy`, mouse
  moves, tab closing, unknown functions) leave a `// TODO` line and a warning,
  and the check is created paused. `basic_auth` credentials go to the check's
  encrypted secrets, not the script. A `fill` value goes to the encrypted
  secrets too (`PINGDOM_FILL_1`, ..., read as `secrets.PINGDOM_FILL_1`) when
  the field looks like a credential field (`password`, `pwd`, `pin`, `otp`,
  `secret`, `token`, `api key`, `mot de passe`, ...) or the value looks like a
  credential (a `Bearer ` token, a long random string). Any other typed value
  stays in the script, and a warning counts them: move any credential to
  `secrets.NAME` before confirming. Pingdom regions are not mapped.
- Basic-auth credentials are **not** imported (a warning lists them): re-enter them on the check.
- Probe filters, alert contacts, integrations and maintenance windows.
