# Agent auto-upgrade

Spec `2026-11-22-04`. Private-location and system agents follow the server's
version: the server asks a drifted agent to restart, and the agent's image
reference, `<server-host>/solidping:current`, is served by the server itself and
resolves to the server's version. Version reporting and the drift badge are in
[deported-agents.md](deported-agents.md) (*Build version*).

## Rolling upgrade

A connected agent whose reported version differs from the server's (neither
side a `dev` build) gets an `upgrade` frame (`MsgTypeUpgrade`, carries
`targetVersion`). Unsolicited and uncorrelated like `upload-request`: an agent
that predates it drops it in its read pump's default case.

- **Agent side** (`checkworker/backend/ws_upgrade.go`, `agentmode.runUntilUpgrade`):
  the backend closes `UpgradeRequested()`, agent mode cancels the worker loop
  (the fetcher stops claiming; runners finish the job they hold, its result is
  submitted on the `saveCtx` fallback, bounded by the job's timeout), closes the
  socket and `Run` returns nil: exit 0. Leases of jobs claimed but not started
  are freed by the next process's new boot ID (spec 2026-11-14-03).
- **One agent per region at a time, across replicas**
  (`handlers/agentws/upgrade.go`, migration `040_v0_54_0`):
  `agent_upgrade_slots` holds one row per region key (`<org uid>|<region>` or
  `system|<region>`: an org region is org-relative), taken with an insert or a
  compare-and-swap on `generation`; `agent_upgrade_candidates` lists the drifted
  agents whose connection refreshes `seen_at` every 30 s (stale after 3
  intervals) with their `attempts` for that target. Next in line: fewest
  attempts, then oldest row, so a timed-out agent is not retried before the
  others. All time comparisons happen in Go (SQLite stores times as text).
- **Release**: an agent reporting the server's version (enroll frame or first
  claim) releases its slot and drops its candidate row, logged at INFO. Timeout
  10 min: the next contender logs a WARN naming the agent that did not come
  back (with the slot's `deadline`, since the expiry is only seen when an agent
  next contends, and `next_agent`), then takes the slot; when that contender is
  the same agent (single-agent region) the WARN says "asking it again" with the
  `attempt` number. A region whose only agent is upgraded is logged at
  INFO. After 3 requests for one version the server stops asking that agent
  (WARN once per connection): a supervisor that restarts on the same image (a
  Docker restart policy, a fly machine) would otherwise restart it every
  10 min forever.
- **Opt-out**: `SP_AGENTS_AUTO_UPGRADE=false` (server config only, no per-org
  setting; system agents follow the same path).
- **Agent-side pin**: `SP_AGENT_IGNORE_VERSION_DRIFT=true` on the agent
  (`config.AgentConfig.IgnoreVersionDrift`). The agent sends `autoUpgrade: false`
  on its enroll and claim frames (`ClientFrame.AutoUpgrade`, a `*bool`;
  `ClientFrame.VersionPinned()`), and ignores any `upgrade` frame
  (`backend/ws_upgrade.go`, one INFO). The server (`noteAgentVersion`) never
  registers a pinned agent as a candidate and its tick never sends it a frame;
  when it was asked before being pinned, `withdrawFromUpgrade` releases its
  slot and deletes its candidate row, so the next agent is asked at once rather
  than after the 10 min timeout. The drift WARN stays. The pin is stored in
  `workers.version_pinned` (refreshed on each connection's first report) and
  exposed as `versionPinned` on the agent lists; dash0 shows a "Pinned" label
  next to "Drifted" (`AgentVersionCell`, test id `<cell>-pinned`).
- **Agents older than the rolling upgrade** (v0.53 and before) send no
  `autoUpgrade` field (`ClientFrame.PredatesAutoUpgrade()`) and do not know the
  `upgrade` frame. The server treats them like pinned agents in the rollout (no
  frame, no slot, `withdrawFromUpgrade`) and logs once per connection at INFO
  `agent predates auto-upgrade: upgrade it by hand`. They are not recorded as
  pinned (`version_pinned` stays false): they keep the plain "Drifted" badge.
- **The image**: `<server-host>/solidping:current` from the read-only registry
  below, pulled again by kubelet with `imagePullPolicy: Always`. Docker restart
  policies reuse the local image: the docs ship a systemd timer running
  `docker compose pull && docker compose up -d`. Fly system agents restart on
  their deployed image, so they still need a `fly deploy` per server upgrade.

## Agent image registry

`internal/agentregistry`, mounted by `app/agent_registry_routes.go` at the host
root (`/v2`, `/v2/`, `/v2/*`, own per-IP limiter at 300/min), never on a custom
status-page domain (`isCustomHostForbidden`). One repository, `solidping`:

- `GET|HEAD /v2/` answers 200 with `Docker-Distribution-API-Version: registry/2.0`.
- `manifests/<ref>`: `current` (the server's version, 404 `MANIFEST_UNKNOWN` on a
  dev build), a release tag (`1.2.3`, `1.2.3-rc.1`; anything else is refused
  before any upstream call) or a `sha256:` digest. Fetched from
  `ghcr.io/v2/fclairamb/solidping` with an anonymous token
  (`/token?scope=repository:fclairamb/solidping:pull`, no GitHub token needed),
  with a fixed `Accept` list (OCI index, Docker manifest list, both manifest
  types) so the answer is cacheable, and returned byte for byte with ghcr's
  `Content-Type` and `Docker-Content-Digest`. The body's sha256 must match the
  announced digest (and the asked digest), else 502. Cached by digest; a tag's
  resolution for 5 min.
- `blobs/<digest>`: 307 to the `Location` ghcr redirects to (signed
  `pkg-containers.githubusercontent.com` URL). Layers never transit the server;
  an upstream that does not redirect is a 502.
- Other repository: 404 `NAME_UNKNOWN`. Upstream unreachable or 5xx: 502
  `UNAVAILABLE`. No auth: the images are public.

**Prototype (2026-11-23, v0.53.0 binary on `localhost:4291`).** Both runtimes
accept the proxied manifest list and the blob 307 to ghcr's CDN:
`docker pull localhost:4291/solidping:current` (Docker Desktop with the
containerd image store) and `ctr -n k8s.io images pull --local --plain-http
host.docker.internal:4291/solidping:current` (containerd's own resolver, the one
the CRI plugin and so kubelet use) both pulled the multi-arch index
`sha256:a02230ce…47f1`, the same digest ghcr serves for `0.53.0`. ghcr's
anonymous token and blob redirect work without a GitHub token.
