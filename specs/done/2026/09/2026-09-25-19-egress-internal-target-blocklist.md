---
model: opus
effort: high
---

# Egress guard: no protection against internal-target SSRF in check targets

## Problem

Any org member can point a check at an internal address and read the response
back through check results:

- `checkhttp/checker.go:241-256` — the HTTP client has **no dial-level IP
  filtering whatsoever**. A check on `http://169.254.169.254/latest/meta-data/iam/security-credentials/`,
  `http://localhost:4000/api/v1/...` or any RFC1918 address works.
- Readback is built in: `capture_failure_response` stores body/status/headers
  into results (`checkhttp/checker.go:362-389`), and `body_expect` /
  `body_pattern` / `json_path_assertions` (`checker.go:437-549`) turn content
  into UP/DOWN signals visible in the org's dashboards.
- Same surface in `checkjs` (scripted `http.get/post/…`, body returned into
  results — `checkjs/checker.go:619,830-882`), `checkprometheus`,
  `checkwebsocket`, `checktcp`, and every check type that dials user targets.

For self-hosted deployments, monitoring internal infrastructure is the
product — this is accepted by design. For **SaaS shared workers**, it is a
cross-tenant / internal-metadata SSRF with a full read primitive, available
to every org member.

There is no DNS-rebinding protection anywhere either: `FamilyDialContext`
(`checkerdef/httptransport.go:67-98`) resolves and dials the chosen IP but
never validates it, so a URL-string-level blocklist alone would be bypassed
by rebinding. The guard must live at the dial layer.

## Proposal

1. New package `server/internal/egress`:
   - `Guard` built from a config knob `egress.allow_private_targets`
     (env `SP_EGRESS_ALLOW_PRIVATE`, system parameter; **default: true** for
     self-hosted, **false** for SaaS workers — read `config.DeploymentMode`).
   - `DialContext` / `Control` wrapper: resolve the hostname once, reject
     loopback, RFC1918 private, link-local unicast (incl. 169.254.0.0/16
     cloud metadata), IPv6 ULA `fc00::/7`, link-local `fe80::/10` and the
     unspecified address; when allowed, dial the **pinned resolved IP** (not
     the hostname again), which also kills DNS rebinding.
   - Clear error surfaced to the check result: `target resolves to a
     non-public address, denied by egress policy`.
2. Wire it into every check-family transport at one choke point:
   `checkerdef/httptransport.go` (covers `checkhttp`, `checkprometheus`,
   `checkwebsocket`, clickhouse, etc.), `checkjs`'s HTTP client
   (`checker.go:830-841`) and socket dialers, `checktcp` / `checkgrpc` /
   `checkkafka` / `checkrabbitmq` TLS/TCP dials. Prefer injecting the dialer
   through the existing transport factory rather than touching every checker.
3. Private-location agents (workers inside the customer network) run with
   `SP_EGRESS_ALLOW_PRIVATE=true` — that is their entire purpose. The check's
   `regions`/worker placement decides which guard applies: shared cloud
   workers deny, customer-hosted agents allow. If worker-mode is already
   distinguishable server-side, drive the default from it; otherwise ship the
   env var for agent operators and document it.
4. Docs: new page on egress policy + changelog entry (behavior change for
   SaaS). The error message must tell the user exactly which parameter an
   operator can flip.

Out of scope: notification senders (spec `2026-09-25-20`), redirects (spec
`2026-09-25-21`), docker sockets (spec `2026-09-25-22`).

## Tests

- Unit tests on the resolver guard: loopback, `169.254.169.254`, RFC1918,
  ULA, link-local, IPv6 forms, hostname resolving to a private IP (rebinding
  simulation), all rejected when `allow_private=false`; all pass when true;
  pinned-IP dial actually connects to the resolved address.
- Integration (both DBs): create an HTTP check targeting a loopback test
  server with `allow_private=false` → execution fails with the egress error;
  with `allow_private=true` → succeeds.
- `checkjs`: script calling `http.get("http://127.0.0.1:…")` under deny →
  result carries the egress error, no body in output.
- Route/placement test: shared cloud workers default to deny, agents default
  to allow.
- Config test: `SP_EGRESS_ALLOW_PRIVATE` in the env-var table.
## Implementation Plan

1. **`server/internal/egress` package** (policy only, no checker imports, reusable
   by specs 20/21/31):
   - `IsNonPublic(ip)` over an explicit prefix table (loopback, RFC1918,
     CGNAT 100.64/10, link-local incl. 169.254.169.254, 0/8, 192.0.0/24,
     198.18/15, multicast, reserved 240/4 + broadcast, `::`, `::1`, ULA
     `fc00::/7`, `fe80::/10`, site-local `fec0::/10`, IPv6 multicast, and
     IPv4-mapped / NAT64 forms judged on their embedded IPv4).
   - `Guard` (`New(allowPrivate, opts…)`, nil-safe): `Enforcing()`,
     `CheckIP`, `CheckHostIP`, `Resolve` (filtered, pinned), `DialContext` /
     `DialContextWith(base)` (resolve once, dial the pinned public IP, never the
     hostname again; `Control` hook re-checks the address actually connected),
     `HTTPTransport()` (one shared pooled guarded transport, proxy disabled).
   - `ErrDenied` sentinel + `*DeniedError` whose message says
     "target resolves to a non-public address, denied by egress policy" and
     names `SP_EGRESS_ALLOW_PRIVATE` / `egress.allow_private_targets`.
   - Context plumbing: `WithGuard` / `FromContext`, plus a per-execution
     denial recorder so the worker can normalize the result.
2. **Config**: `config.EgressConfig{AllowPrivateTargets *bool}` (koanf
   `egress.allow_private_targets`, tri-state), manual env reader for
   `SP_EGRESS_ALLOW_PRIVATE` (listed in `manualReaderEnvVars`), and
   `Config.EgressAllowsPrivateTargets()` resolving: explicit value > agent node
   role (allow) > SaaS (deny) > self-hosted (allow). System parameter
   `egress.allow_private_targets` (same env var) in `systemconfig`.
3. **Worker wiring** (`checkworker`): build the guard once in
   `newCheckWorker` (shared by in-process workers and deported agents, so the
   decision is made in the process that dials), put it on `execCtx` before
   the SSH tunnel is set up, and normalize a result whose execution recorded
   a denial (StatusError, egress error message, no network-failure address so
   no traceroute is requested). Local traceroute dispatch and the agent-side
   trace handler also refuse non-public addresses under an enforcing guard.
4. **Choke points in `checkerdef`**: `OutboundDialer(ctx)` /
   `OutboundDialerOr(ctx, base)` (tunnel dialer > enforcing guard > default)
   and `GuardDialerOr(ctx, base)` for UDP/non-tunnel paths;
   `HTTPTransportFor(ctx, skipTLS)` replaces the tunnel-only
   `BuildHTTPTransport` call in checkhttp / checkprometheus / checkjs;
   `FamilyDialContext` gains the guard; `ResolveFailureStatus` maps
   `egress.ErrDenied` to StatusError.
5. **Wire every dialing checker**: http, prometheus, websocket, js (http,
   sockets, websocket, browser pre-flight), tcp, ssl, udp, grpc, kafka,
   rabbitmq, clickhouse, postgres, mysql, mssql, oracle, mongodb, redis, mqtt,
   ftp, sftp, ssh, smtp, imap, pop3, rdp, sip, snmp, ntp, a2s, minecraft,
   icmp, dns / dnsbl (custom nameserver only), freebox_line, kubernetes
   (rest.Config.Dial), browser (pre-flight on the URL host), SSH-tunnel
   bastion dial. Deliberately unguarded (documented): the system DNS
   resolver, domain (RDAP/WHOIS servers chosen by IANA/registries), passive
   types (heartbeat, email, private_location), sleep, docker (spec 22).
6. **Tests**: egress unit tests (every range, IPv6 forms, rebinding via a fake
   lookup, pinned dial connects to the resolved address, Control hook);
   config env/param tests; placement default tests (saas worker deny, agent
   allow, self-hosted allow, explicit override); checkhttp + checkjs + tcp
   execution tests against a loopback server under deny/allow; a worker
   execution test through `executeJob` on both DBs.
7. **Docs**: `web/docs` egress policy page (+ sidebar), config reference
   entry. No CHANGELOG edit (release-please); the SaaS behaviour change goes
   in the commit body.
