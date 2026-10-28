---
model: sonnet
effort: medium
---

# ICMP checks fail on fly.io agents: no raw socket, no ping group

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/27

## Source

In-app report on the maintainer's instance (v0.42.0): an ICMP check of `1.1.1.1`
has had an open, escalated incident for more than two days, 3519 failures, all
from the `tokyo` region, which is staffed by a fly.io agent. The first failure:

> listen ip4:icmp 0.0.0.0: socket: operation not permitted

The reporter asks why the fly.io instance shows this and how to fix it. The target
is up: this is a false outage caused by the region, and it pages.

## Problem

- `listenICMP` (`server/internal/checkers/checkicmp/burst.go:589`) first tries an
  unprivileged datagram ICMP socket (`udp4`), then a raw one (`ip4:icmp`). The
  error reported is the raw fallback's, so both failed.
- The datagram socket needs the process gid inside `net.ipv4.ping_group_range`;
  the raw socket needs `CAP_NET_RAW`. The image runs as the distroless `nonroot`
  user (65532, `Dockerfile` final stage), and a fly machine is a Firecracker VM
  with the kernel default `ping_group_range = "1 0"` (nobody). Docker and
  Kubernetes hide this: Docker opens the ping group for containers, and the Helm
  chart adds `NET_RAW` (`deploy/helm/solidping/templates/deployment.yaml:68-72`).
  `deploy/fly/fly.toml` does neither, and its README never mentions ICMP.
- Nothing stops an incapable worker from claiming ICMP checks. The region
  capability system (`server/internal/regions/capabilities.go`: `ipv4`, `ipv6`,
  `browser`, `discovery`) has no `icmp` entry, so placement and the dashboard
  cannot know that a region cannot ping. Every ICMP check placed there fails,
  opens an incident and pages.

## Why a spec

- An infrastructure decision for the fly deployment (see Open questions), not
  verifiable from this checkout.
- A new worker capability: worker report, region aggregation, placement filter,
  dashboard warning. Several subsystems, over the direct-fix size.

## Proposal

1. **Make ICMP work on fly.** Per the open question, either:
   - a. ship `deploy/fly/Dockerfile` (`FROM ghcr.io/fclairamb/solidping:<tag>`,
     `USER 0:0`) and point `fly.toml` `[build]` at it: root in the VM has
     `CAP_NET_RAW`; or
   - b. keep the image and have agent mode, at startup, when it runs as root,
     widen `/proc/sys/net/ipv4/ping_group_range`. Not possible as nonroot, so this
     still needs a.
   Document it in `deploy/fly/README.md` (a "ICMP checks" section with the error
   above and the fix), and point the private infra repo's fly apps at it.
2. **An `icmp` worker capability.** At worker start (server worker and agent),
   probe both socket kinds once (open and close, IPv4 and IPv6) and report
   `icmp` in `workers.capabilities` like `browser`. Aggregate it in
   `regions/capabilities.go` (`models.CapabilityICMP`), with the same three
   states (yes / no / unknown).
3. **Use it.** ICMP checks require `icmp` in placement (`regions/placement.go`,
   like browser checks), and the check form shows the same region warning as the
   browser and IPv6 cases (`check-region-browser-capability.spec.ts` is the
   model). A worker whose probe said no must not claim ICMP jobs.
4. **A clearer result when it still happens**: the check result says the region
   cannot open an ICMP socket (missing `CAP_NET_RAW` or ping group) rather than
   the raw Go error, and links the docs.
5. Tests: unit test of the capability probe with an injected listener; placement
   test (icmp check skips a region reporting `icmp: no`); e2e for the warning.

## Open questions

1. **Fly fix: run the agent as root (1a), or something else?** Recommended: 1a,
   the agent image only for fly, root inside a single-tenant Firecracker VM, no
   inbound port. Trade-off: the process runs as root; the alternative, a custom
   init that sets the sysctl then drops privileges, is more code for a VM that
   holds nothing but the agent key.
2. **Placement when no region of a check can ping**: refuse the save, or save
   with a warning? Recommended: warn and save, like browser checks, so a
   temporary outage of the capable regions does not block edits.

## Closing the issue

This spec closes fclairamb/solidping-business#27. The implementing PR body
**must** carry `Closes fclairamb/solidping-business#27`. When the spec is archived
to `specs/done/`, verify the issue is closed and, if the merge did not close it,
close it by hand:

    gh issue close 27 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

1. Run the agent as root through a fly-only image (1a). (autopilot: recommended answer)
2. Warn and save when no region of a check can ping. (autopilot: recommended answer)
