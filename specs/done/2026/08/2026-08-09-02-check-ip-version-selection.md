---
model: opus
effort: high
---

# Checks can't choose an address family, so dual-stack targets are silently only ever monitored over IPv4

## Problem

Every non-HTTP checker resolves the target and then **hard-prefers IPv4**, using
IPv6 only when no A record exists at all. The pattern is copy-pasted across nine
checkers, e.g. [`checktcp/checker.go:130-146`](../../server/internal/checkers/checktcp/checker.go):

```go
// Use any of the resolved addresses (prefer IPv4 if available)
var targetIP net.IP
var isIPv6 bool

for i := range addrs {
	if addrs[i].IP.To4() != nil {
		targetIP = addrs[i].IP
		isIPv6 = false
		break
	}
}

// Fall back to IPv6 if no IPv4 found
if targetIP == nil {
	targetIP = addrs[0].IP
```

The same `To4() != nil`-first loop appears in
[`checkssl:63`](../../server/internal/checkers/checkssl/checker.go),
[`checkimap:402`](../../server/internal/checkers/checkimap/checker.go),
[`checkudp:103-109`](../../server/internal/checkers/checkudp/checker.go),
[`checkdns:264`](../../server/internal/checkers/checkdns/checker.go),
[`checkdnsbl:253`](../../server/internal/checkers/checkdnsbl/checker.go),
[`checkssh:115-119`](../../server/internal/checkers/checkssh/checker.go),
[`checksmtp:496`](../../server/internal/checkers/checksmtp/checker.go),
[`checkpop3:416`](../../server/internal/checkers/checkpop3/checker.go) and
[`checkicmp:131-148`](../../server/internal/checkers/checkicmp/checker.go).

HTTP is the exception: it builds a bare `&http.Client{}` with a nil `Transport`
([`checkhttp/checker.go:255`](../../server/internal/checkers/checkhttp/checker.go)),
so it inherits `http.DefaultTransport` and Go's Happy Eyeballs — which prefers
IPv6. So the product is inconsistent *between check types* on the same host, and
uncontrollable in both.

The consequence is the one that matters to users: **an IPv4 check does not verify
IPv6 reachability.** A dual-stack target whose AAAA path is broken — bad AAAA
record, missing firewall rule, dead v6 route, misconfigured load balancer — keeps
reporting `up` on every check type except HTTP, while real IPv6 users are down.
There is no way to express "monitor this host over IPv6", short of hardcoding a
literal address and losing DNS coverage entirely.

`ip_version` already exists in the codebase, but only as an **output** field —
[`checktcp:167`](../../server/internal/checkers/checktcp/checker.go),
[`checkudp:135`](../../server/internal/checkers/checkudp/checker.go),
[`checkicmp:206`](../../server/internal/checkers/checkicmp/checker.go) report which
family they happened to pick. It is never an input. Note that the three checkers
already reporting it are exactly the ones where users most expect to control it.

This is now load-bearing: check `c0437dd8-65bd-4767-9956-6c899eef8b5b`
(org `acmetech`) exists specifically to validate IPv6 reachability, and today it
can only do so because its target is IPv6-*only*. The moment a target is
dual-stack, the check silently stops testing IPv6.

### Prior art

| Tool | Mechanism | Values | Default | Scope |
|:--|:--|:--|:--|:--|
| [Better Stack](https://betterstack.com/docs/uptime/api/update-an-existing-monitor/) | `ip_version` monitor field | `ipv4`, `ipv6`, `null` | `null` = use both | All monitor types (edge-based infra) |
| [Gatus](https://github.com/TwiN/gatus) | `client.network` | `ip`, `ip4`, `ip6` | `ip` | ICMP endpoints only |
| [Uptime Kuma](https://github.com/louislam/uptime-kuma/issues/6121) | `ip_family` monitor column | v4 / v6 / auto | auto | Partial; [long tail of IPv6 issues](https://github.com/louislam/uptime-kuma/issues/7258) |

Better Stack's model is the one to copy: a per-monitor `ip_version` with a
tri-state where the unset value means "no constraint". Gatus's ICMP-only scoping is
the mistake to avoid — the problem is not ICMP-specific, and a per-type option
would be arbitrary. Uptime Kuma is mostly a cautionary tale: the feature was
retrofitted per-monitor-type and accumulated a long tail of bugs (including a DB
column type error on save), which argues for doing address selection **once, in
shared code**, rather than nine times.

## Proposal

Add a per-check `ip_version` **input** option, resolved in one shared place.

### 1. Shared resolution helper

The type-agnostic precedent already exists:
[`checkerdef.ExtractTargetHost`](../../server/internal/checkers/checkerdef/targethost.go)
deliberately uses a flat convention across all checkers rather than a per-type
switch, so a new checker gets correct behaviour for free. Follow that design.

Add to [`checkerdef`](../../server/internal/checkers/checkerdef/resolve.go) an
`IPVersion` type (`auto` / `ipv4` / `ipv6`, zero value `auto`) plus a helper that
takes the resolved `[]net.IPAddr` and the requested version and returns the chosen
address:

- `auto` — **preserve today's per-checker behaviour exactly** (IPv4-first for the
  nine listed checkers, Happy Eyeballs for HTTP). This is a compatibility
  requirement, not a preference: silently flipping existing checks to IPv6 would
  change results on every dual-stack target in every install.
- `ipv4` / `ipv6` — filter to that family; if none is available, fail the check
  with a clear, distinguishable error (see §4) rather than falling back.

Then replace the nine copy-pasted loops with calls to it. The existing
`ip_version` *output* should report the family actually used, unchanged in shape.

### 2. Config surface

Add `IPVersion string \`json:"ip_version,omitempty"\`` to each checker's config
struct (`server/internal/checkers/check*/config.go`). Prefer a shared embedded
struct over nine independent fields if the existing config-parsing path allows it —
that is the concrete lesson from Uptime Kuma's retrofit.

Applies to: tcp, udp, icmp, ssl, ssh, smtp, imap, pop3, dnsbl, http. Explicitly
**not** applicable to types with no network target (heartbeat, email, sleep) — the
same set `ExtractTargetHost` already returns nil for.

`checkdns` needs a decision, called out in §6.

### 3. HTTP needs a transport, not an address pick

HTTP is structurally different: it dials by name inside `http.Transport`, so it
can't use the shared address-pick helper. Constrain it by setting a `Transport`
whose `DialContext` forces network `tcp4` / `tcp6`.

This must compose with the existing tunnel path, which is the *only* reason
`Transport` is set today
([`checkhttp/checker.go:271+`](../../server/internal/checkers/checkhttp/checker.go)):
an SSH-tunneled check dials through a worker-supplied dialer, where the address
family is the tunnel's business, not the check's. Decide and document the
interaction — most likely `ip_version` is rejected at validation time when the
check is tunneled, rather than silently ignored.

### 4. Error reporting

"No address of the requested family" is a distinct, user-actionable failure
(usually a missing AAAA record) and must not be reported as a generic connection
failure. Give it its own entry in the
[checker error catalogue](../../server/internal/checkers/checkerdef/ERRORS.md)
following the conventions in that file, with a message naming the host and the
requested family.

### 5. API, UI and docs

- Expose `ip_version` in the check config schema and the per-type sample configs
  (`server/internal/checkers/check*/samples.go`).
- Validation: accept only `auto` / `ipv4` / `ipv6` (plus empty = `auto`), rejecting
  anything else at write time with an actionable message.
- dash0: a tri-state selector in the check form, defaulting to Auto. Surface the
  resolved family on the check detail page — the `ip_version` output field is
  already produced by tcp/udp/icmp and should be shown wherever it exists.
- Docs: document the option, its default, and the "an IPv4 check does not verify
  IPv6 reachability" rationale — the reason someone would set it.

### 6. Open questions

- **`checkdns`** already queries A and AAAA and splits them by `To4()`
  ([`checkdns/checker.go:237-287`](../../server/internal/checkers/checkdns/checker.go)).
  For a DNS check, `ip_version` could plausibly mean either "which record types to
  assert on" or "which transport to reach the nameserver over" — these are
  different features. Pick one, or scope DNS out of this spec and say so in the
  docs.
- **Monitoring both families on one check.** Better Stack's `null` means *use
  both*, whereas the `auto` proposed here means *pick one, as today*. Genuinely
  checking both families in a single check would need two probes and a rollup
  status — a larger feature. Recommend explicitly deferring it: a user wanting
  both coverage today creates two checks. Worth confirming this is the intended
  product behaviour before implementing, since it is the one place this spec
  knowingly diverges from the prior art it otherwise follows.
- **Region interaction.** A worker whose node has no IPv6 will fail every
  `ip_version: ipv6` check assigned to it. After
  `2026-08-09-01-split-checks-role-from-api-jobs.md` lands, some regions may still
  lack v6 egress. Decide whether that surfaces as a per-check failure (simple, but
  noisy and blames the target) or as a worker capability advertised at
  registration so the scheduler can avoid the assignment (correct, but a much
  larger change). Recommend the former for this spec, with the error text making
  clear the *worker* had no IPv6 — but flag it, because a user seeing "down" for
  an infrastructure gap is a bad first impression of the feature.

## Resolved open questions

**Q: "`checkdns` … For a DNS check, `ip_version` could plausibly mean either
'which record types to assert on' or 'which transport to reach the nameserver
over' … Pick one, or scope DNS out of this spec and say so in the docs."**

**Decision: scope DNS out of this spec.** `ip_version` does not apply to
`checkdns` — leave `checkdns/checker.go`'s existing A/AAAA handling exactly as
it is. Reject or ignore `ip_version` on DNS checks (pick whichever matches how
the other unsupported per-type options behave — be consistent, and cover it with
a test), and say plainly in the docs that DNS checks do not take `ip_version`,
because the two possible meanings are different features. Do not implement
either meaning here; a follow-up spec can add whichever turns out to be wanted.

**Q: "Monitoring both families on one check … `auto` proposed here means *pick
one, as today* … Worth confirming this is the intended product behaviour."**

**Decision: confirmed — defer both-family monitoring.** `auto` means "pick one,
exactly as today" and must reproduce current behaviour byte-for-byte. A user who
wants both families creates two checks. Do NOT build two-probe execution or a
rollup status. This is a knowing divergence from Better Stack, where `null`
means *use both*: **the importer must not silently mis-map it** — when a Better
Stack check carries the both-families value, either emit an explicit importer
warning saying only one family will be monitored, or map it to `auto` and warn.
Follow whatever the surrounding importer warning convention is, and cover it
with a test.

**Q: "Region interaction. A worker whose node has no IPv6 will fail every
`ip_version: ipv6` check assigned to it … per-check failure … or a worker
capability advertised at registration."**

**Decision: per-check failure, with the error naming the worker.** Do not touch
worker registration, the capability model, or job assignment. When a worker
cannot use the requested family, the check fails with its own catalogued error
whose text makes unambiguously clear that **the worker had no IPv6 egress** —
not that the target is down. Word it so a user reading it once knows to look at
their region/worker rather than their own service, and assert that wording in a
test. A capability-aware scheduler is explicitly a follow-up.

## Acceptance criteria

- [ ] `ip_version: ipv6` on a dual-stack target checks it over IPv6, verified by
      the `ip_version` output field.
- [ ] `ip_version: ipv4` on a dual-stack target checks it over IPv4.
- [ ] Unset / `auto` reproduces current behaviour byte-for-byte on every affected
      checker — including HTTP's Happy Eyeballs and the nine checkers' IPv4-first
      pick.
- [ ] Requesting a family the target has no address for fails with its own
      catalogued error naming the host and family, not a generic dial failure.
- [ ] Address-family selection lives in one shared helper; no checker retains its
      own `To4() != nil` preference loop.
- [ ] Invalid `ip_version` values are rejected at write time.
- [ ] The option is settable in dash0 and the resolved family is visible on the
      check detail page.
- [ ] The tunnel/`ip_version` interaction is explicitly handled and documented,
      not left implicit.

## Implementation Plan

Chosen shape: `ipVersion` is a **shared, well-known check-config key**, not a field on
ten config structs — the `tunnelCheckUid` / `timeout` precedent
(`checkerdef/tunnel.go:56-66`). The worker reads it generically off the raw config
map and puts it on the execution context; checkers consume it through one helper.
This is the strongest reading of "address-family selection lives in ONE shared
helper", and it means a checker gaining support needs no config-struct change.

Canonical key `ipVersion` (camelCase, per `wiki/conventions/checker-config.md` /
`resolveKey`), with `ip_version` accepted as a snake_case read fallback. The
**output** key stays `ip_version`, unchanged in shape.

1. **`server/internal/checkers/checkerdef/ipversion.go` (new)** — the single home:
   `IPVersion` type (`auto`/`ipv4`/`ipv6`, zero value auto), `ParseIPVersion`,
   `IPVersionFromConfig` (camel + snake fallback), `WithIPVersion`/`IPVersionFrom`
   context plumbing, `MatchesIPVersion`, `Network("tcp") -> tcp/tcp4/tcp6`, and
   **`SelectIPAddr(host, addrs, version)`** — the one address pick.
   - `auto`: first IPv4 (normalized via `To4()`), else `addrs[0]`. Byte-for-byte
     what all nine loops do today, including checkicmp's 4-byte normalization.
   - `ipv4`/`ipv6`: filter; none → `ErrNoAddressForFamily` naming host + family.
   - explicit family only: a local-egress pre-flight (`Dial("udp6", ip:9)`, no
     packets sent) turns "this worker has no IPv6 route" into
     `ErrWorkerNoEgress`, whose text blames the worker/region, not the target.
     Injectable via `SelectIPAddrWithProbe` for tests; never runs for `auto`.

2. **Capability metadata** — `CheckTypeMeta.SupportsIPVersion`
   (`json:"supportsIpVersion"`) for tcp, udp, icmp, ssl, ssh, smtp, imap, pop3,
   dnsbl, http; echoed by `handlers/checktypes/service.go`. Everything else
   (including **dns**, per the resolved question) rejects the key, exactly like
   `supportsTunnel` does.

3. **Worker** (`internal/checkworker/worker.go`) — after the tunnel block, put a
   non-auto `IPVersion` on `execCtx`. `auto` leaves the context untouched.

4. **Checkers** — delete every `To4() != nil` preference loop and call
   `checkerdef.SelectIPAddr`: checktcp, checkudp, checkicmp, checkssl, checkssh,
   checksmtp, checkimap, checkpop3. checkdnsbl's IPv4 filter is a *protocol*
   constraint (DNSBL zones index IPv4), expressed via `MatchesIPVersion`; it
   accepts `auto`/`ipv4` and rejects `ipv6` at write time. checkdns is untouched.
   **checkhttp** cannot pick an address (it dials by name): it forces
   `Network()` on a `Transport.DialContext` when non-auto, and reports the
   family actually connected via `httptrace.GotConn` (so `auto` keeps
   `Transport == nil` → `DefaultTransport` → Happy Eyeballs, unchanged).

5. **Write-time validation** — `handlers/checks/ipversion.go`:
   `validateIPVersionConfig(checkType, config)` rejects an unknown value, the key
   on a type that does not support it, `ipv6` on dnsbl, and **any non-auto value
   on a tunneled check** (the tunnel resolves remotely; the family is the
   bastion's business). Wired into the same three sites as
   `validateConfigTimeout` (validate / create / PATCH) plus the document
   validator.

6. **Better Stack importer** — Better Stack's `null` means *both families*;
   SolidPing's `auto` means *pick one*. Map `ipv4`/`ipv6` through, and warn
   explicitly whenever the monitor leaves it unset that only one family will be
   monitored.

7. **dash0** — `CheckTypeInfo.supportsIpVersion`, an `IPVersionSelect` in the
   Advanced section (`check-ip-version-section` / `check-ip-version-select`),
   serialized into the config alongside `tunnelCheckUid`, and the resolved
   family shown as its own labelled row on the check detail page.

8. **Docs** — `web/docs/docs/features/check-types.md` Common Options: the option,
   its default, the "an IPv4 check does not verify IPv6 reachability" rationale,
   the DNS exclusion, and the tunnel interaction.
