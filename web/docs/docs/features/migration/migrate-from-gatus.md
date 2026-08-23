---
sidebar_position: 1
slug: /features/migrate-from-gatus
title: Migrate from Gatus
description: Import your Gatus config.yaml endpoints into SolidPing as checks, in one paste.
---

# Migrate from Gatus

SolidPing imports a [Gatus](https://github.com/TwiN/gatus) `config.yaml` directly:
every entry of the `endpoints:` list becomes a SolidPing check, conditions and all.
Nothing has to be recreated by hand.

## Where to find your configuration

Gatus has no configuration-export API — its runtime API returns statuses, not
endpoint definitions — so the YAML file itself is the source of truth.

- **Docker / Compose**: the file you mount at `/config/config.yaml`.
- **Kubernetes**: the ConfigMap the Gatus pod mounts
  (`kubectl get configmap gatus-config -o jsonpath='{.data.config\.yaml}'`).
- **Binary install**: `config/config.yaml` next to the binary, or whatever
  `GATUS_CONFIG_PATH` points at.

If you split your config across a `config/` directory, concatenate the files that
contain `endpoints:` entries into one document before importing.

## Import it

1. Open **Checks** in the dashboard and click **Import**.
2. Pick **Gatus (config.yaml)** as the source.
3. Paste the YAML, or click **Upload a file**.
4. Click **Import preview**. Nothing is written yet: you see exactly what would be
   created, what would be updated, and everything that did not map cleanly.
5. Review the warnings, then confirm.

Re-importing the same file later is safe — checks are matched by slug and updated
in place, never duplicated.

Prefer the API? It is one call:

```bash
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/yaml' \
  --data-binary @config.yaml \
  'https://your-instance/api/v1/orgs/myorg/checks/import/convert?source=gatus&dryRun=true' | jq '.'
```

Drop `&dryRun=true` to apply. The endpoint requires an **organization admin** token.

## What maps

| Gatus | SolidPing |
|---|---|
| `endpoints[].url` scheme `http(s)://` | an `http` check |
| `tcp://`, `tls://`, `starttls://` | a `tcp` check (TLS on for `tls`/`starttls`) |
| `udp://` | a `udp` check |
| `icmp://`, `ping://` | an `icmp` check |
| `ssh://` | an `ssh` check |
| `ws://`, `wss://` | a `websocket` check |
| a `dns:` block | a `dns` check (`url` becomes the resolver) |
| `name`, `group`, `enabled` | check name/slug, check group, enabled flag |
| `interval` | check period |
| `method`, `body`, `headers`, `client.timeout` | the HTTP request settings |

### Conditions

| Gatus condition | SolidPing |
|---|---|
| `[STATUS] == 200` | expected status |
| `[STATUS] == any(200, 301)`, `[STATUS] == 2XX` | expected status codes |
| `[BODY] == value` / `!= value` | body must contain / must not contain |
| `[BODY] == pat(*glob*)` | body regex |
| `[BODY].path == value` (and `!=`, `>`, `>=`, `<`, `<=`) | a JSONPath assertion |
| `has([BODY].path) == true/false` | an `exists` / `not_exists` assertion |
| `[CERTIFICATE_EXPIRATION] > 48h` | a separate `ssl` check with that threshold |
| `[DOMAIN_EXPIRATION] > 720h` | a separate `domain` check with that threshold |
| `[CONNECTED] == true`, `[DNS_RCODE] == NOERROR` | implicit — every SolidPing check already asserts reachability |

## What does not map

These are reported as warnings on the import preview; the check is still imported.

- **`[RESPONSE_TIME]` and `[IP]` conditions** — SolidPing records response time as a
  metric rather than a pass/fail condition.
- **`len([BODY].path)` conditions** — no direct equivalent.
- **`alerts:`** — notification bindings are not imported. Wire up SolidPing
  [integrations](/docs/features/incidents) after the import.
- **`external-endpoints:`** — Gatus push endpoints. Recreate them as SolidPing
  heartbeat checks.
- **`client.insecure` / `client.ignore-redirect`** — SolidPing verifies certificates
  and follows redirects.
- **SSH credentials** — deliberately never imported. SolidPing has fields for
  them, but an import must not silently re-persist secrets copied out of a
  foreign config file. Re-enter them on the check.

## After the import

Checks created this way carry the label `solidping.io/managed=gatus`, so you can
filter on them and re-run the import as your Gatus config evolves.
