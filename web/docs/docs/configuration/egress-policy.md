---
sidebar_position: 5.5
title: Egress Policy
---

# Egress policy

A check connects to a target someone in the organization chose. On a
self-hosted instance, monitoring your internal network is the whole point. On
a shared, multi-tenant deployment it is not: a check aimed at
`http://169.254.169.254/latest/meta-data/` or `http://localhost:4000/` would
read the worker's own cloud credentials or internal API back into its results.

The egress policy decides whether the check workers of a process may connect
to **non-public addresses**.

## Defaults

| Where the check runs | Private targets |
|---|---|
| Self-hosted (`SP_DEPLOYMENT_MODE=self-hosted`, the default) | Allowed |
| SaaS shared workers (`SP_DEPLOYMENT_MODE=saas`) | **Denied** |
| [Private-location agent](/features/private-locations) (`SP_NODE_ROLE=agent`) | Allowed |

The policy is evaluated in the process that dials, from its own configuration.
A check placed on a private location runs on your agent and may reach your
network. The same check placed on a shared SaaS region runs on a shared worker
and is refused.

## Configuration

| Variable | System parameter | Default | Description |
|---|---|---|---|
| `SP_EGRESS_ALLOW_PRIVATE` | `egress.allow_private_targets` | derived (see above) | `true` lets this process's check workers reach non-public addresses, `false` refuses them. Unset or empty keeps the derived default |

The environment variable wins over the system parameter. The parameter is read
at startup, so restart the workers after changing it. Agents have no database
and only read the environment variable.

## What is refused

A target is refused when it **resolves** to one of these ranges:

| Range | What it is |
|---|---|
| `127.0.0.0/8`, `::1` | Loopback |
| `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` | RFC 1918 private networks |
| `100.64.0.0/10` | Carrier-grade NAT, used as internal space by some clouds |
| `169.254.0.0/16` | Link-local, including the `169.254.169.254` cloud metadata endpoint |
| `fc00::/7` | IPv6 unique local addresses |
| `fe80::/10`, `fec0::/10` | IPv6 link-local and deprecated site-local |
| `0.0.0.0/8`, `::/96` | "This network", the unspecified address and deprecated IPv4-compatible IPv6 (`::a.b.c.d`) |
| `192.0.0.0/24`, `198.18.0.0/15`, `100::/64` | Protocol assignments, benchmarking, discard |
| `224.0.0.0/4`, `ff00::/8`, `240.0.0.0/4` | Multicast, reserved and broadcast |

IPv4-mapped IPv6 addresses (`::ffff:10.0.0.1`), NAT64 addresses
(`64:ff9b::/96`) and 6to4 addresses (`2002::/16`) are judged by the IPv4
address they carry.

## How it works

The check is guarded where the connection is made, not on the URL string:

1. The hostname is resolved **once**.
2. Every non-public answer is dropped. If nothing public is left, the check is
   refused.
3. The connection goes to the IP that was just checked, never to the hostname
   again. A DNS answer that changes between the check and the connection (DNS
   rebinding) cannot slip through.
4. The socket re-checks the address it actually connects to, as a last line of
   defense.

Redirects, extra connections (FTP passive mode, Kafka brokers advertised by the
cluster, database drivers) and JavaScript checks (`http`, `tcp`, `udp`,
websockets, sub-checks) all go through the same guard.

A refused check reports **Error** (not Down) with this message:

```
target resolves to a non-public address, denied by egress policy: internal.acme.com;
an operator can allow private targets on this worker with SP_EGRESS_ALLOW_PRIVATE=true
(system parameter egress.allow_private_targets)
```

The message names the host you configured, never the address it resolved to:
on a shared worker that address comes from the worker's internal DNS. The
resolved address is only written to the server log.

The result also carries `egress_denied: true`. No path trace is ever run
towards a refused address.

## Per check type

| Check types | Guarded |
|---|---|
| http, prometheus, websocket, tcp, ssl, udp, grpc, ssh, sftp, ftp, smtp, imap, pop3, rdp, sip, ntp, snmp, a2s, minecraft, icmp | The target host |
| postgresql, mysql, mssql, oracle, mongodb, redis, clickhouse, kafka, rabbitmq (incl. the management API), mqtt | Every connection the driver opens |
| js | Every `http`, socket and websocket call, and every sub-check |
| dns, dnsbl | A custom `nameserver`. The worker's own system resolver is not a target and is not guarded |
| freebox_line, kubernetes | The box's base URL, the cluster's API server |
| browser, js `browser.open()` | The navigated URL before Chrome loads it, and the address of every response while the page loads (see limits) |
| Any check with an [SSH tunnel](/features/ssh-tunnels) | The bastion. What the bastion reaches is its own network, not the worker's |
| domain | Not guarded: it queries the registries' RDAP and WHOIS servers, not a host you choose |
| heartbeat, email, private_location | Not guarded: passive, they make no outbound connection |

## Limits

### Browser checks

Chrome has its own network stack and resolver, so it cannot be handed the
guard. Under an enforcing policy a browser check (or a script's
`page.goto()`) gets three layers instead:

1. **Before navigating**, the URL is read the way Chrome reads it. Only
   `http`/`https` URLs with a host are accepted (`http:127.0.0.1/` is
   refused). Legacy IPv4 spellings (`2130706433`, `0x7f000001`,
   `0177.0.0.1`, `127.1`) are judged as the address they mean. A host that
   resolves to a non-public address is refused, and so is a host that does
   not resolve at all.
2. **The approved address is pinned** for Chrome's resolver, so a DNS
   answer that changes after the check (rebinding) never reaches Chrome.
   This only happens when SolidPing starts Chrome itself for the check (no
   `SP_CHECKERS_BROWSER_CDP_URL`). A remote Chrome is shared by every check
   and its flags are fixed when it starts, so it cannot be pinned per check.
   A script's `browser.open()` starts Chrome before the script says where it
   will go, so script navigations are not pinned either.
3. **While the page loads**, the address Chrome actually connected to is
   checked for every response and every redirect: the main document,
   subresources, `fetch`/XHR calls. The first non-public one fails the check
   with the egress error. Nothing read from the page after that (title,
   text, `evaluate`, screenshot) reaches the result.

What this still does not stop:

- **The request itself.** Layer 3 sees a response after it arrived. When
  layers 1 and 2 could not apply (a remote Chrome that got a rebound DNS
  answer, a redirect or subresource pointing at a private address, a
  script's own navigation), Chrome has already sent that request. The
  content never reaches the result, but a request with side effects has
  happened.
- **Requests that get no response** (connection refused, timeouts) are not
  seen by layer 3. Their timing can still tell whether an internal address
  answers.
- **WebSocket connections opened by the page** do not report the address
  they connected to and are not checked.

### Outside the check workers

The policy covers checks executed by the workers. A few calls about the same
targets run in the API or jobs process and are not guarded yet:

- Kubernetes cluster discovery and connection validation.
- The Freebox pairing calls made when you connect a box.
- Notification senders (webhooks and similar).

These are tracked separately.

### Scope

The policy is per process. A SaaS operator who needs a shared worker to reach
a private address should run that check from a private-location agent instead
of opening the whole worker.
