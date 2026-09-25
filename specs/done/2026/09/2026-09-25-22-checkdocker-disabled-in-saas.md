---
model: sonnet
effort: low
---

# checkdocker hands any org member the server's local Docker socket in SaaS mode

## Problem

`checkdocker/config/config.go:151-152` accepts any endpoint starting with
`unix://` or `tcp://`. In SaaS deployments the shared worker typically runs
inside a container that mounts `/var/run/docker.sock`, so any org member can
create a docker check against `unix:///var/run/docker.sock` and read
container listings/metadata back through org-visible check results — a
local-socket read primitive on shared infrastructure.

Self-hosted, monitoring the local Docker daemon is the feature working as
intended; the fix is SaaS-only.

## Proposal

1. In `checkdocker` config validation, reject the check with
   `VALIDATION_ERROR` ("docker checks are not available on this deployment")
   when `config.DeploymentMode == DeploymentModeSaaS` —
   `internal/config/config.go:72-76` already distinguishes the modes.
   - This covers unix:// AND tcp:// endpoints: in SaaS mode the whole checker
     type is disabled, per the product decision.
2. Existing docker checks keep their rows but fail execution at runtime with
   the same message when the deployment is SaaS (guard in the checker or in
   the job dispatcher, whichever is the established pattern for type-level
   gating — mirror how any other SaaS-restricted feature is enforced, e.g.
   entitlement-driven checkers).
3. The check-type catalog / schema endpoint should stop advertising
   `docker` in SaaS mode so the dashboard hides it (`gen/checkerschema` is
   build-time; gate at the API that lists available check types instead).
4. Docs + changelog entry; call out that private-location agents running in
   the customer's own network are unaffected — if a SaaS customer wants
   Docker monitoring, an agent in their network is the supported path
   (agents run the checker themselves, so the mode gate must apply to
   server-side execution only, not to agent-mode workers).

Verify step 4 against how agent-mode workers execute checks: if the check
runs entirely on the customer's agent, the runtime guard must key on *where*
execution happens, not on deployment mode alone.

## Tests

- SaaS-mode server: creating a docker check → 400 `VALIDATION_ERROR`.
- SaaS-mode server: a pre-existing docker check executes → result fails with
  the explicit "not available" error, no socket contacted.
- Self-hosted mode: docker check creation and execution still work (test
  with a unix socket fixture if one exists, else config-validation only).
- Check-types listing in SaaS mode omits `docker`; in self-hosted it
  includes it.
- Agent-executed docker checks (private locations) are not blocked.