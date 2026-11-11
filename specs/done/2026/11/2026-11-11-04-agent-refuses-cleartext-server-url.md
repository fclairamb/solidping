---
model: sonnet
effort: medium
---

# A private agent connects to its server over plain HTTP without a word, so anyone on the path becomes its server

## Problem

Found by the security scan of 2026-11-11 (finding AJ4).

- **Severity:** Low.
- **Who can trigger it:** an attacker on the network path between a private agent and its server (same LAN,
  rogue Wi-Fi, DNS spoofing). It needs the agent operator to set an `http://` server URL.

`SP_AGENT_SERVER_URL` takes `http(s)://host[:port]` (`server/internal/config/config.go:201-204`). The only check is
that it is set (`config.go:3831`). The agent dials `<url>/api/v1/agent/ws`
(`server/internal/checkworker/backend/ws.go:836`), and the capture and task HTTP calls use the same base
(`ws_capture.go:182-190`).

The agent signs its frames with its Ed25519 key, but the server never proves who it is: TLS is the only server
authentication. Over plain HTTP, an on-path attacker can stand in for the server and:

- with peer re-wrap on (the default, #835), hand the agent a `rewrap` task with its own recipient. The executor
  re-seals every blob it can open to that key (`server/internal/rewraptask/executor.go:37-62`), which defeats
  browser sealing;
- relay real jobs with the target changed, so the agent sends the unsealed secrets to the attacker (AJ1 class,
  spec `2026-11-11-01`);
- feed the agent arbitrary checks to run inside the agent's network.

The docs always show `https://`, but nothing refuses `http://` or warns about it.

## Proposal

1. In config validation (`config.go`, next to `ErrAgentServerURLRequired`), refuse an agent `ServerURL` whose scheme
   is not `https` (or `wss`).
   - Allow `http`/`ws` only for a loopback host (`localhost`, `127.0.0.0/8`, `::1`, which `make dev` and the e2e
     side-car use), or when `SP_AGENT_ALLOW_INSECURE_SERVER=true` is set.
   - With that override, log a startup warning every time, and turn peer re-wrap off unless
     `SP_AGENT_REWRAP_ENABLED=true` is set explicitly.
2. Document the variable in `web/docs/docs/features/private-locations.md` and the agent env var list (envcheck).

## Tests

- Go, `server/internal/config`: agent role with `http://master.acme.com` → validation error naming the variable.
  With `SP_AGENT_ALLOW_INSECURE_SERVER=true` → accepted, and rewrap is off by default. `http://localhost:4000` and
  `http://127.0.0.1:4000` → accepted. `https://master.acme.com` → accepted (positive control).
- Make sure the existing agent e2e and the `make dev` agent flow (loopback) still pass.
