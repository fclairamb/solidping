---
sidebar_position: 19
title: Traceroute diagnostics
---

# Traceroute diagnostics

When a check goes down because its target could not be **reached**, SolidPing
runs an MTR-style traceroute from the probing location and attaches the result
to the incident. The hop table sits on the incident page next to the rest of the
evidence: hop number, host, packet loss and average round-trip time, with the
probe mode and the region it ran from labelled.

The point is to answer the question an error string cannot: *where* on the path
did it break.

## What triggers a capture

A trace is taken **only on the transition to down** — the probe that opens (or
reopens) the incident — and **only for network-reachability failures**:

| Failure | Traced |
|---|---|
| Connect timeout (no SYN-ACK, no refusal) | yes |
| Connection refused | yes |
| ICMP timeout or destination unreachable | yes |
| TLS handshake stalled after the connection came up | yes |
| HTTP 4xx/5xx | **no** |
| Keyword / body assertion mismatch | **no** |
| Certificate expiring, self-signed, or for the wrong name | **no** |
| DNS name does not resolve | **no** |

The second half of that table is not an oversight. In every one of those cases
the target **answered** — the path is demonstrably fine, and a hop list would be
noise on the incident page. A name that does not resolve has no address to trace
to at all; resolution diagnostics are a separate feature.

Two more consequences worth knowing:

- **A check that flaps produces one trace per outage, not one per failing
  probe.** A 30-second check failing all day opens one incident and gets one
  capture, not 2,880.
- **A probe dialed through an [SSH tunnel](./ssh-tunnels.md) is never traced.**
  The failure happened on the far side of the bastion, so a trace from the
  worker would describe a route the packet never took.

## Turning it on and off

The default is **on**. There are two levers, and the check's own answer wins:

- **Per check** — *Advanced → Traceroute on network failure*: `Inherit
  organization default`, `Always capture`, or `Never capture`.
- **Per organization** — *Organization → Settings → Network path diagnostics*.
  This is what `Inherit` resolves to.

Self-hosters have a third, deployment-wide switch:

| Variable | Default | Description |
|---|---|---|
| `SP_CHECKERS_TRACEROUTE_ENABLED` | `true` | Kill switch. Turns the feature off everywhere; it never turns it on for a check that opted out. |
| `SP_CHECKERS_TRACEROUTE_ROUNDS` | `3` | Probes sent per hop. More rounds means better loss figures and a longer capture. |
| `SP_CHECKERS_TRACEROUTE_HOPS` | `30` | Maximum TTL. |
| `SP_CHECKERS_TRACEROUTE_BUDGET` | `15s` | Hard ceiling for one capture, reverse DNS included. |
| `SP_CHECKERS_TRACEROUTE_LIMIT` | `10` | Traces started per minute **per organization**. |

The per-organization limit is the mass-outage guard. When a single upstream
drops, every check behind it opens an incident inside the same minute; without a
ceiling that would be one 15-second sweep per check, all reporting the same
broken hop and all competing for the egress the monitoring itself needs.

## It never delays anything

The failing result is written and the incident is opened **first**. The trace
starts afterwards, on its own goroutine, with the hard time budget above, and
attaches when it finishes. A trace that fails — no privilege, no route, budget
exhausted, storage unavailable — costs you the capture and nothing else. The
incident, the notification and the status page are identical either way.

## Probe modes, and what each one can see

Traceroute needs to send packets with a lowered TTL and read the ICMP replies
that come back. How much of that a host is allowed to do depends on its
privileges, so SolidPing detects what it can actually open at startup and picks
the best available mode. **The mode is recorded in every capture and labelled in
the UI**, because the three see genuinely different things.

| Mode | Requires | Sees |
|---|---|---|
| **ICMP (privileged)** | `root` or `CAP_NET_RAW` | Every router that answers TTL-exceeded, with per-hop loss and RTT. |
| **ICMP (unprivileged)** | An ICMP datagram socket — the default on macOS; on Linux the process gid must be inside `net.ipv4.ping_group_range` | The same hops as privileged ICMP. |
| **TCP (reachability only)** | Nothing | **No intermediate hop addresses at all** — only how far the SYN got, and therefore how many hops away the target is. |

### Why the TCP mode cannot show you hops

A plain `connect()` gives your program an error number, not the address of the
router that produced it. Recovering that address needs `IP_RECVERR`, which is
Linux-specific. So in TCP mode an empty address column means *"we could not have
heard the routers even if they had answered"* — **not** *"the routers stayed
silent"*. Those two look identical in a hop table and mean opposite things,
which is why the incident page says so explicitly whenever a capture came from
this mode. Do not read a TCP-mode table as a broken path.

The compensation is real, though: TCP mode probes the check's **own target
port**, so it crosses the same firewalls the check does. A path that passes ICMP
and blocks 443 looks perfect in the other two modes and broken in this one — and
this one is right.

:::note Classic UDP traceroute is not one of the options
The familiar `traceroute` that sends UDP datagrams to high ports still needs a
raw socket to *read* the ICMP replies, so it is not an unprivileged mode. The
unprivileged tier that actually exists is the ICMP datagram socket above.
:::

## Granting the privilege (self-hosted)

If your incidents show *TCP (reachability only)* and you want real hop lists,
give the SolidPing process one of the following.

**Docker / Docker Compose**

```yaml
services:
  solidping:
    image: ghcr.io/fclairamb/solidping:latest
    cap_add:
      - NET_RAW
```

**Kubernetes**

```yaml
securityContext:
  capabilities:
    add: ["NET_RAW"]
```

**A plain binary on Linux**

```bash
sudo setcap cap_net_raw+ep /usr/local/bin/solidping
```

**Unprivileged alternative on Linux** — allow the ICMP datagram socket instead
of granting a raw one. This is the smaller permission of the two:

```bash
# Allow every group (0 through 2147483647) to open ICMP datagram sockets.
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
```

Narrow the range to the gid SolidPing runs as if you would rather not open it to
everyone. On macOS this socket is available to unprivileged processes already,
so a development machine usually needs nothing.

**Nothing is required.** With no privilege at all the feature keeps working in
TCP mode, and a check with no port to fall back on (an `icmp` check on a host
that cannot open an ICMP socket) simply gets no capture. SolidPing never fails a
check, an incident, or a worker over a socket permission.

## Distributed and private locations

A check that runs on a [private location](./private-locations.md) is traced **by
that agent**, not by the server. That is the whole point: the agent sits inside
the network whose path failed, and a trace from the SolidPing server would
describe a completely different route. The agent runs the sweep and uploads the
result over the same signed attachment endpoint it uses for screenshots.

If the agent's connection has dropped in the meantime, the capture is simply
lost — the server will not substitute a trace from its own vantage point, because
that trace would be confidently wrong.

Privileges are per-agent: an agent in a container with no `NET_RAW` falls back to
TCP mode exactly as the server would, and its captures are labelled accordingly.

## Reading the capture

- **Loss on an intermediate hop is usually not the problem.** Many routers
  deprioritize or rate-limit the ICMP they generate for expired TTLs while
  forwarding traffic perfectly. Loss that starts at one hop and *continues to
  the target* is the interesting pattern; loss at a single hop with clean hops
  after it is almost always the router being polite about ICMP.
- **The last hop that answered is where the path stops working**, when the trace
  never reached the target.
- **A hop showing several addresses** is a load-balanced (ECMP) path; different
  probes took different routers. That is normal.
- **`truncated`** means the time budget ran out. What is shown is real, but it is
  not the whole path.

The raw JSON is downloadable from the incident page if you want to diff two
captures or feed one into another tool.
