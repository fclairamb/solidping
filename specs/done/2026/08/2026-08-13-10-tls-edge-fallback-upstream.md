---
model: opus
effort: xhigh
---

# TLS edge fallback upstream — forward unknown-SNI/Host connections to a second server over PROXY protocol v2

## Problem

The deployed topology has a single dynamic-custom-domain slot at the edge:
Traefik terminates TLS for hostnames it knows and TCP-passthroughs everything
else (its `HostSNI(`*`)` catch-all) to ONE SolidPing instance, which runs
in-server ACME (`server/internal/tlsedge/`) and issues certificates on demand
for its own verified custom domains. Traefik cannot host a second catch-all,
and it cannot enumerate either instance's custom domains — that knowledge lives
in each instance's database.

The operator wants to run TWO instances behind the same edge: the **prod**
instance as the primary Traefik fallback, chaining to the **dev** instance for
custom domains prod does not own. Today an unknown host on prod dead-ends: the
on-demand decision func (`Edge.decide`, `server/internal/tlsedge/edge.go:205`)
refuses issuance (`ErrHostNotAllowed`) and the handshake fails. There is no way
for prod to hand the connection to a next hop.

## Proposal

Teach the TLS edge an optional **fallback upstream**: when a connection arrives
for a host that is neither a reserved host nor a servable custom domain on this
instance, forward the *raw* connection to a configured upstream, prefixed with
a PROXY protocol v2 header carrying the original client address. Each hop stays
fail-closed on certificate issuance for the domains it owns; the last hop
refuses unknown hosts exactly as today.

### Config

New keys in the `acme.*` block (`config.ACMEConfig`,
`server/internal/config/config.go`), mirroring the existing
`proxy_protocol` naming:

- `acme.fallback_upstream_https` (`SP_ACME_FALLBACK_UPSTREAM_HTTPS`) —
  `host:port` to receive forwarded unknown-SNI TLS connections. Empty (default)
  = feature off, behavior unchanged.
- `acme.fallback_upstream_http` (`SP_ACME_FALLBACK_UPSTREAM_HTTP`) — idem for
  plaintext `:80` traffic (ACME HTTP-01 + the https redirect for the
  downstream's domains). Empty = no HTTP forwarding.
- `acme.fallback_upstream_proxy_protocol` (bool, default `true` when an
  upstream is set) — send a PROXY v2 header on the outbound connection.
- Startup validation in `config.validate` alongside
  `validateACMEProxyProtocol`: upstreams must parse as `host:port`; setting
  either upstream requires `acme.enabled`.

Multi-word koanf keys need the manual env reader (see
`internal/config/envvars.go` — same quirk as `proxy_protocol_trusted_cidrs`).

### HTTPS path — SNI peek below TLS

The fork must happen **below** TLS termination — prod does not hold the
downstream's certificates. In `tlsedge`, wrap the HTTPS listener (after the
inbound `wrapProxyProtocol`, `server/internal/tlsedge/proxyproto.go:44`) with a
splitter that, per accepted connection:

1. Reads the TLS ClientHello with a read deadline (reuse `readHeaderTimeout`,
   `edge.go:44`), handling a hello fragmented across TCP segments. Parse only
   as far as the SNI extension; keep every byte read for replay.
2. Decides locally: reserved host or `CustomDomainServable(ctx, sni)` true
   (`Options.CustomDomainServable`, `edge.go:79`, wired from
   `app/tls_edge.go:61`) → hand the connection (peeked bytes replayed via a
   prefix-reader `net.Conn` wrapper) to the existing `http.Server`+certmagic
   stack, unchanged. The 60s cache in `statuspages` keeps random-SNI scans off
   the DB.
3. Otherwise → dial `fallback_upstream_https` (bounded dial timeout, ~5s),
   write a PROXY v2 header whose source is `conn.RemoteAddr()` (already
   rewritten to the real client by the inbound go-proxyproto wrapper), replay
   the buffered ClientHello, then splice both directions until EOF/error
   (`io.Copy` both ways, close on first error, idle guard).
4. No SNI in the hello, or the peek times out / fails to parse → local stack
   (which will refuse issuance as today). Never forward on a parse failure.

`github.com/pires/go-proxyproto` (already a dependency) provides
`proxyproto.HeaderProxyFromAddrs` + `Header.WriteTo` for step 3. For step 1,
either a minimal in-repo ClientHello reader (the `crypto/tls`-based
peek-without-handshake pattern) or a small vetted helper — implementer's
choice, but no TLS termination may occur before the decision.

### HTTP path — Host peek

Same splitter shape on the `:80` listener, keyed on the HTTP `Host` header:
peek the request head (bounded by `readHeaderTimeout` and a size cap, e.g.
8 KiB), extract `Host`, apply the same local-vs-forward decision, replay the
buffered bytes. Local connections continue into the existing
`challengeHandler` (`edge.go:380`). Without this, the downstream can never
solve HTTP-01 challenges for its domains (TLS-ALPN-01 would still ride the
:443 path, but both must work).

### Safety rails

- **Loop guard**: refuse to forward when the immediate peer address equals the
  configured upstream (a chain misconfigured into a cycle must fail fast, not
  ping-pong until fd exhaustion). Log once per offending peer.
- **Downstream trust**: document (runbook) that the downstream instance must
  add this instance's egress IPs to `SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS` —
  the existing inbound trust mechanism is unchanged.
- **Failure = close**: upstream unreachable → close the client connection
  (indistinguishable from today's failed handshake for an unknown host). Never
  fall back to local termination for a host we already decided is not ours.
- **Observability**: slog line + counters for forwarded / refused / dial-failed
  connections on both listeners.

### Testing

- Unit: ClientHello peek (whole, fragmented, no-SNI, garbage, timeout); Host
  peek (normal, oversized head, missing Host); loop guard; config validation.
- Integration (pattern of `tlsedge/proxyproto_test.go` and
  `acme_e2e_test.go`): two in-process edges chained — connect with an SNI only
  the downstream serves, assert the downstream sees the ORIGINAL client
  address via PROXY v2 and completes issuance/serving; assert a host the
  upstream owns never reaches the downstream; assert unknown-everywhere hosts
  are refused by the last hop.
- HTTP-01 path: plaintext request for a downstream domain reaches the
  downstream's challenge handler through the chain.

### Out of scope

- Traefik/k8xp manifests (ops change, runbook note only — extend
  `wiki/runbooks/custom-domain-tls.md` with the chained-fallback option).
- More than one upstream / routing tables — a single next hop chains
  arbitrarily deep already.
- Forwarding for the main plain-HTTP app listener (`:4000`) — only the two
  ACME edge listeners take part.

---

## Implementation Plan

1. **Config** (`server/internal/config/config.go`, `envvars.go`)
   - Add to `ACMEConfig`: `FallbackUpstreamHTTPS`, `FallbackUpstreamHTTP`,
     `FallbackUpstreamProxyProtocol` (default `true`), and
     `FallbackUpstreamDialTimeout` is *not* added — the dial timeout stays a
     package constant (~5s) per the Proposal.
   - Read the four multi-word keys in `applyACMEEnv` (literal `os.Getenv`
     calls, so the `envvars_test.go` registry guard sees them) and register the
     names in `manualReaderEnvVars`.
   - `validateACMEFallbackUpstream`, called from `validateACMEConfig`:
     each upstream must parse as `host:port` with a non-empty host and a
     numeric port; setting either upstream requires `acme.enabled`.
     New sentinel errors `ErrACMEFallbackUpstreamInvalid` /
     `ErrACMEFallbackUpstreamRequiresACME`.

2. **Peek layer** (`server/internal/tlsedge/peek.go`)
   - `recordingConn` — records every byte read from the client and swallows
     writes, so no TLS alert is ever sent and nothing is consumed destructively.
   - `peekTLSClientHello` — `crypto/tls.Server` + `GetConfigForClient` that
     captures `ServerName` and aborts with a sentinel error before any
     termination. Bounded by a read deadline (`readHeaderTimeout`).
   - `peekHTTPHost` — reads the request head up to `\r\n\r\n`, capped at 8 KiB,
     parses it with `http.ReadRequest` and returns `Host`.
   - `prefixConn` — replays every peeked byte ahead of the live connection.

3. **Splitter** (`server/internal/tlsedge/fallback.go`)
   - `fallbackListener` — `net.Listener` wrapper: an accept loop hands each raw
     connection to a goroutine that peeks, decides, and either queues it on the
     channel `Accept` drains (local) or forwards it (upstream). No peek ever
     blocks the accept loop.
   - Local on: no host, peek error/timeout, oversized head, or
     `hostIsLocal` (reserved host or `CustomDomainServable`). Never forward on
     a parse failure.
   - Forward: loop guard → dial (5s) → PROXY v2 header from
     `conn.RemoteAddr()`/`LocalAddr()` (+ hop-count TLV) → replay peeked bytes →
     splice both directions with `CloseWrite` half-close and a bounded
     linger deadline on the surviving half.
   - Failure (loop guard, dial failure, header write failure) closes the client
     connection — never falls back to local termination.
   - Loop guard: peer IP equals the upstream IP (spec rule), plus a PROXY v2
     hop-count TLV that bounds a cycle even when every hop shares one IP
     (loopback), where the address rule cannot discriminate. Logged once per
     offending peer.
   - Counters: per-listener `local` / `forwarded` / `refused` / `dial_failed`
     as in-process atomics (for tests) and as
     `solidping_tlsedge_connections_total{listener,outcome}` in `prommetrics`.

4. **Wiring** (`server/internal/tlsedge/edge.go`)
   - Extract `hostIsLocal` out of `decide` and reuse it for the split decision.
   - `listen` takes the listener kind, and wraps with the splitter *after*
     `wrapProxyProtocol` when the matching upstream is configured. No upstream
     configured = listener untouched = today's behavior bit for bit.

5. **Tests**
   - `peek_test.go` — ClientHello whole/fragmented/no-SNI/garbage/timeout/
     immediate-EOF; Host peek normal/fragmented/oversized/missing/garbage;
     `prefixConn` replay.
   - `fallback_test.go` — loop-guard comparison table; splitter routes local vs
     forwarded; dial failure closes the client; counters; config validation
     (in `config_test.go`).
   - `fallback_chain_test.go` — real `Edge` in front of a fake downstream
     (PROXY v2 listener): the downstream sees the ORIGINAL client address, the
     replayed ClientHello completes a real handshake, a host the upstream owns
     never reaches the downstream, unknown-everywhere hosts are refused by the
     last hop, and the plaintext HTTP-01 path traverses the chain with its
     `Host` intact. Two chained real edges for the `:80` challenge-handler path.
   - `acme_e2e_test.go` — docker-gated chained Pebble issuance: the CA
     validates against the UPSTREAM's ports, both challenges are forwarded, and
     the DOWNSTREAM completes real issuance and serves its own certificate.

6. **Docs** — chained-fallback section in `wiki/runbooks/custom-domain-tls.md`
   (including the downstream-trust requirement) and the four new keys in the
   `custom-domains` / `configuration` docs tables.
