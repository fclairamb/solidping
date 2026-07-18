---
sidebar_position: 12
title: SSH Tunnels
---

# SSH tunnels

Probe services that are only reachable through a **bastion host** — private
networks, VPC-internal services, a database behind a jump box — with nothing new
to deploy. If you already run SSH, you already have everything you need.

A tunnel-capable check carries a reference to an **SSH check** in the same
organization. At each execution the worker opens a fresh SSH session to that
bastion and dials the check's target through it.

```
worker ──SSH──▶ bastion ──▶ internal-api.private:8080
```

## When to use this vs. a private location

Both reach things the cloud workers cannot:

| | SSH tunnel | [Private location / agent](./private-locations.md) |
|---|---|---|
| Setup | Point at an existing SSH check | Deploy and enroll an agent |
| Needs inbound access | Yes — the bastion's SSH port | No — the agent dials out |
| Protocols | Any TCP-based check (databases, brokers, `http`, `tcp`, …) | Every check type, incl. ICMP/UDP |

Reach for the tunnel when a bastion already exists; reach for an agent when
nothing is reachable from outside at all.

## Setting one up

1. **Create an SSH check for the bastion.** Give it a `username` plus a
   `password` or `private_key`, and — required — its `expected_fingerprint`.
2. **On the check you want to tunnel**, open **Advanced → Run through SSH
   tunnel** and pick the SSH check.

The bastion is now monitored in its own right, and it is the single home for
its credentials: they are stored encrypted at rest on that one check, and there
is no separate tunnel resource to keep in sync.

## Supported check types

Every TCP-based check type can run through a tunnel — the classic bastion use
cases (a database or message broker on a private network) are all covered:

- **Web & network:** `http`, `tcp`, `ssl`, `websocket`, `grpc`
- **Databases:** `postgresql`, `mysql`, `mssql`, `oracle`, `redis`, `mongodb`
- **Message brokers:** `rabbitmq`, `kafka`, `mqtt`
- **Mail:** `smtp`, `imap`, `pop3`
- **File transfer:** `ftp`

The dashboard's selector appears only on types that support it (the API reports
this as `supportsTunnel` on `/api/v1/orgs/{org}/check-types`), so the option
shows up automatically wherever it applies.

UDP- and ICMP-based types (`icmp`, `udp`, `ntp`, `snmp`, `dns`, `dnsbl`, `sip`,
`a2s`) cannot tunnel: an SSH `direct-tcpip` forward carries **TCP only**. For
those, use a [private location / agent](./private-locations.md) instead.

:::note FTP passive mode
For `ftp`, both the control connection and passive-mode (PASV) data connections
are dialed through the bastion. One caveat: a tunneled **implicit-TLS** FTPS
check connects in plaintext through the tunnel — use explicit TLS (`AUTH TLS`)
if you need the control channel encrypted end to end.
:::

## Host-key verification is required

An SSH check may be used as a tunnel **only if its `expected_fingerprint` is
set**, and the tunnel dial verifies the bastion's host key against it strictly.
The tunnel carries your probe's traffic, so an unverified bastion is a silent
man-in-the-middle. SSH checks with no fingerprint stay selectable-but-disabled
in the picker, with a hint.

A plain SSH check without a fingerprint keeps working as a check — the
requirement applies only when it is used as a tunnel.

## Names are resolved by the bastion

The target hostname is sent through the tunnel **verbatim** and resolved on the
bastion, not by the worker. That is the point: `internal-api.private` means
nothing to a cloud worker's resolver, but resolves correctly on the far side —
so private DNS names work exactly as they do when you SSH in yourself.

## Latency: `tunnel_setup_ms`

Establishing the SSH session is **not** counted in the check's response time. It
is recorded as its own metric, `tunnel_setup_ms`, in the result's metrics.
Without that split, every tunneled check's latency graph would be a picture of
SSH handshakes rather than of the service you are monitoring.

Each execution dials its own session, so expect a `tunnel_setup_ms` on every
result. (Connection pooling is a future improvement.)

## Failure classification

A failure of the **tunnel** is reported distinctly from a failure of the
**target**:

| What happened | Result |
|---|---|
| Bastion unreachable, auth failed, host key mismatch, forward refused | `error`, output `tunnel failed: …` plus a `tunnel_failed: true` marker. The target is never probed. |
| Tunnel fine, target refused / timed out / returned the wrong thing | The check's normal `down` / `timeout` result |

So a broken bastion reads as a broken bastion, not as ten services going down at
once. (Automatically suppressing dependents' alerts when the bastion is down is
a planned follow-up built on this distinction.)

## Rules and limits

- The referenced check must be **in the same organization**, must be an **`ssh`**
  check, and must set **`expected_fingerprint`**.
- **No chaining** — the referenced SSH check may not itself be tunneled.
  Multi-hop bastions are a possible follow-up.
- **Deleting a bastion that other checks tunnel through is refused** (`409`),
  listing the dependents. Detach them first. The SSH check's detail page shows
  the same list.
- The dependency is **config-level, not runtime-level**: disabling or pausing
  the SSH check does **not** stop the checks that tunnel through it, and their
  regions are independent of its regions.
