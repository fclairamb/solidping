---
model: opus
effort: high
pr: batch
automerge: false
---

# Private-location agents drift from the server's version and nothing brings them back

## Problem

Agents report their build version (spec `2026-08-19-07`). The server logs one WARN on drift
(`server/internal/handlers/agentws/handler.go:639`, `warnOnVersionDrift`) and dash0 shows an
amber "Drifted" badge. That is detection only. `wiki/features/deported-agents.md` lists "no agent
auto-update" as a known gap.

Worse, every install snippet pins `ghcr.io/fclairamb/solidping:latest`:
- `web/docs/docs/features/private-locations.md:59` (and the compose / k8s examples around lines 187 and 253)
- `web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx:425`
- `web/dash0/src/routes/orgs/$org/organization.private-locations.register.tsx:551`, `:555`, `:581`

A self-hosted operator who pins an older server gets agents newer than their server. Nobody gets
agents that follow the server on upgrade or rollback.

## Proposal

Ship in this order. Step 1 is useful on its own.

1. **Exact tag in the snippets.** dash0 snippets print `ghcr.io/fclairamb/solidping:<server version>`
   (from `useVersion()`) instead of `:latest`, falling back to `:latest` on a `dev` build. Same
   wording in the docs page ("use the tag matching your server").

2. **Prototype the registry path before building it.** Check by hand that `docker pull` and
   containerd (`ctr`/`crictl`, i.e. kubelet) accept a manifest list proxied from ghcr by another
   host, then a 307 on blobs to ghcr's signed CDN URL. Record the result in the spec or the wiki.
   If one of them rejects it, stop and report: the steps below depend on it.

3. **Minimal read-only OCI registry at the host root** (new package, e.g. `server/internal/agentregistry`,
   routed in `server/internal/app`, next to `routeMgmtVersion` in `custom_domain_routing.go:59`):
   - `GET|HEAD /v2/` answers 200 with `Docker-Distribution-API-Version: registry/2.0`.
   - `GET|HEAD /v2/solidping/manifests/<ref>`: `<ref>` is `current` (resolved to the server's own
     version), the explicit version tag, or a `sha256:` digest. The server fetches the manifest
     from ghcr with an anonymous pull token and returns it **byte for byte**, with ghcr's
     `Content-Type` and `Docker-Content-Digest`. Cache by digest (manifests are immutable) and the
     `current` resolution for a few minutes.
   - `GET|HEAD /v2/solidping/blobs/<digest>`: 307 to the signed URL ghcr redirects to. Layers
     never transit the server.
   - Any other repository name: 404 with the registry error shape (`{"errors":[{"code":"NAME_UNKNOWN",…}]}`).
   - On a `dev` / untagged build, `current` answers 404 `MANIFEST_UNKNOWN`.
   - No auth: these images are public. Rate-limit per IP like other public endpoints.
   - Not on custom status-page domains (`custom_domain_routing.go`): only the main host.

4. **Server version in `hello`.** Add `ServerVersion string `json:"serverVersion,omitempty"`` to
   `ServerFrame` (`server/internal/agents/protocol.go:188`), set from `version.Get().Version`.
   Older agents ignore it.

5. **Rolling `upgrade` frame.** New server frame `MsgTypeUpgrade = "upgrade"`
   (`protocol.go:24`). When a connected agent reports a version different from the server's, and
   neither side is `dev`, the server asks it to upgrade:
   - **At most one agent per region upgrading at a time**, across all server replicas. The
     coordination must be shared (a DB row or advisory lock keyed by region), not in-memory.
   - It waits for that agent to reconnect reporting the server's version, with a timeout (e.g.
     10 min). On timeout it logs a WARN naming the agent and moves on to the next one; it does
     not retry the same agent before the others.
   - A region with a single agent is still upgraded (brief gap); log it at INFO.
   - Agent side (`server/internal/agentmode/agentmode.go`): on `upgrade`, stop claiming, finish
     in-flight jobs (bounded by their timeout), close the socket and exit 0. Agents that don't know
     the frame ignore it (same as `jobs-available`).
   - Opt-out: server config `SP_AGENTS_AUTO_UPGRADE` (default `true`). See open question for per-org.

6. **Docs and snippets switch to the server registry**: `<server-host>/solidping:current`, with
   `imagePullPolicy: Always` in the k8s / Helm examples (kubelet re-pulls on every container
   restart, so exiting is enough). For plain Docker, say clearly that restart policies reuse the
   local image, and document one updater: a systemd timer running `docker compose pull && docker
   compose up -d`. Mention that the registry needs HTTPS and the host root (no path prefix).

7. **Wiki**: `wiki/features/deported-agents.md`, the "Build version" section and both "no agent
   auto-update" entries (competitive gaps and known gaps).

## Tests

- `agentregistry`: `GET /v2/` 200; `manifests/current` returns the exact bytes, content type and
  digest of a fake upstream; explicit tag and digest work; unknown repo 404 `NAME_UNKNOWN`;
  `current` on a `dev` build 404; blob returns 307 to the upstream's `Location`; upstream down
  gives 502, not a 200 with a bad body.
- `agents/protocol_test.go`: `hello` carries `serverVersion`; an old agent decoding an `upgrade`
  frame ignores it.
- `agentws` handler: drifted agent gets `upgrade`; same-version agent does not; `dev` on either
  side does not; two drifted agents in one region get it one after the other, not together (and
  with two handler instances sharing one DB); timeout moves on; `SP_AGENTS_AUTO_UPGRADE=false`
  sends nothing.
- `agentmode`: on `upgrade` the agent stops claiming, submits the in-flight result, exits 0.
- dash0 Playwright: the register page snippet shows the server's version tag, not `:latest`.

## To verify

- Whether ghcr's anonymous token endpoint and the blob redirect work without a GitHub token from
  the server (step 2 prototype).
- Whether the `/v2/` path conflicts with any existing route or the SPA fallback.
- How `agentmode` exits today and whether the job loop can drain cleanly.

## Open questions

1. Opt-out scope: server config only, or also a per-org setting so a SaaS customer can pin their
   agents? **Recommended: server config only for now**, the SaaS server always wants its agents on
   its version; add per-org when someone asks.
2. Should system agents (platform-operated, `SP_SYSTEM_AGENT_ENROLLMENT_TOKENS`) get the same
   rollout? **Recommended: yes**, same code path.

## Resolved open questions

1. Opt-out scope: server config only. No per-org setting for now. (autopilot: recommended answer)
2. System agents (`SP_SYSTEM_AGENT_ENROLLMENT_TOKENS`) get the same rollout, same code path. (autopilot: recommended answer)
