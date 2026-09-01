---
model: opus
effort: high
---

# gRPC check: real phase breakdown, request metadata, and a complete form

## Problem

We already ship a gRPC health check
([server/internal/checkers/checkgrpc/checker.go](../../server/internal/checkers/checkgrpc/checker.go))
that calls `grpc.health.v1.Health/Check` with host/port, optional
`serviceName`, TLS / `tlsSkipVerify` / plaintext h2c, and tunnel support.
openstatus just launched the same feature
(https://www.openstatus.dev/changelog/grpc-monitoring) and their design
highlights what ours is missing:

1. **`connection_time_ms` is fiction.** `grpc.NewClient` is lazy — it does not
   dial. The metric recorded right after it returns
   (`checker.go:103`) measures struct construction (~0 ms); the real DNS +
   TCP + TLS cost is buried inside `rpc_time_ms` of the first RPC. openstatus
   reports the same phase breakdown as an HTTP monitor (DNS, connect, TLS
   handshake, time to first byte); we report two numbers that are individually
   wrong even though their sum is right. (Repo precedent note: the HTTP
   checker deliberately keeps `Metrics` nil — `checkhttp/checker_test.go:1014`
   — so gRPC carrying a metrics map at all is already ahead; don't block on
   HTTP-side parity.)

2. **Error classification is mush.** Because the dial happens inside the RPC,
   a DNS failure, a connection refusal, and a TLS handshake error all surface
   as `"health check failed: rpc error: ..."` via `handleRPCError`
   (`checker.go:207`) — status `down`, not distinguishable from an unhealthy
   service. The HTTP checker went through exactly this refinement already
   (`checkhttp/checker.go` netfailure classification). The most common
   misconfiguration — checking a named service the server never registered —
   returns gRPC `NOT_FOUND` and today reads as a raw
   `rpc error: code = NotFound desc = …` instead of saying what it means.

3. **No request metadata.** A health endpoint behind an authenticating proxy
   (`authorization: Bearer …`, `x-api-key: …`) cannot be checked. HTTP checks
   solved this with `headers` + encrypted `secretHeaders`
   (`checkhttp/config.go:519` `SecretFields`); gRPC has no equivalent.

4. **The dash0 form is a stub, and the docs undersell.** `grpcModule` in
   [web/dash0/src/components/checks/form/types/messaging.tsx](../../web/dash0/src/components/checks/form/types/messaging.tsx)
   exposes only host/port/serviceName/tls. `tlsSkipVerify` and `timeout` exist
   in the backend config but cannot be set from the dashboard — so "TLS
   without certificate verification", which openstatus advertises, is API-only
   for us — and metadata will need UI too. The docs
   (`web/docs/docs/features/check-types.md:574`) don't mention `tlsSkipVerify`
   either.

One thing openstatus calls out that we already do right and must not lose:
a `NOT_SERVING` response keeps its measured latency (`checkHealth` records
`rpc_time_ms`/`total_time_ms` before the status branch), so a service can be
seen slowing down before it drains. Lock that in with a test.

Also worth noting: the existing `keyword`/`invertKeyword` fields
(`checker.go:181`) match against the serving-status **enum string**
(`SERVING`/`NOT_SERVING`), which is redundant with the status check itself —
see Decisions.

## Proposal

### Backend — phase instrumentation (`checkgrpc`)

- Establish the connection **eagerly and instrumented**, instead of letting
  the first RPC absorb it:
  - a custom `grpc.WithContextDialer` that resolves DNS itself
    (`net.Resolver`, timed → `dns_time_ms`) and then dials the resolved
    address (timed → `connect_time_ms`);
  - a wrapping `credentials.TransportCredentials` whose `ClientHandshake`
    times the TLS handshake → `tls_time_ms` (absent for h2c);
  - after `grpc.NewClient`, drive `conn.Connect()` +
    `conn.WaitForStateChange` until `Ready` (bounded by the check timeout) so
    connection failures are observed *as* connection failures.
- The health RPC then becomes a clean TTFB analog: keep `rpc_time_ms`
  (RPC start → response) and `total_time_ms`. Drop the bogus
  `connection_time_ms` (raw metrics are per-execution jsonb; nothing
  aggregates it under that name today — verify before removing).
- Classify failures by phase in `output` (dns / connect / tls-handshake /
  rpc), mirroring the spirit of checkhttp's netfailure refinement, reusing
  `checkerdef`'s helpers (`checkerdef/netfailure.go`) where they fit. A
  timeout keeps `StatusTimeout`; a `NOT_SERVING` / `SERVICE_UNKNOWN` response
  stays `StatusDown` **with metrics populated**.
- Friendlier failure text for the named-service case: in `handleRPCError`,
  special-case `codes.NotFound` → output error
  `service "<name>" is not registered with the health server`, keeping the
  raw RPC error in a secondary field.
- Tunneled checks (`TunnelDialerFrom`) skip local DNS by design (remote-side
  resolution through the bastion): record no `dns_time_ms` there and keep the
  existing passthrough-resolver behavior byte-for-byte.

### Backend — request metadata

- New config keys on `GRPCConfig`
  ([config.go](../../server/internal/checkers/checkgrpc/config.go)):
  `metadata` (map[string]string, plain, queryable) and `secretMetadata`
  (same shape, encrypted at rest). Implement
  `SecretFields() []string { return []string{"secretMetadata"} }` following
  `HTTPConfig.SecretFields`, and update
  `registry/secret_audit_test.go` which currently asserts checkgrpc exposes
  no secret-bearing fields.
- Validate keys (lowercase per gRPC convention; reject the reserved `grpc-`
  prefix).
- Send both maps on the Check RPC via `metadata.NewOutgoingContext`
  (secret values merged at execute time; never echoed into `output`).

### Frontend — complete the form

- Extend `grpcModule` (messaging.tsx): `tlsSkipVerify` checkbox (shown only
  when TLS is on, with the same warning styling other TLS-skipping checks
  use), `timeout`, and a metadata key/value editor. Reuse the HTTP module's
  headers + secret-headers pattern (`types/http.tsx`, including the
  `headersDirty` guard that prevents wiping stored secrets on every save).
- Locale keys for all four locales (`web/dash0/src/locales/{en,fr,de,es}`);
  run `bun run test:unit` — locale-completeness is tested there, not in e2e.

### Docs & samples

- Update the gRPC entry in `web/docs/docs/features/check-types.md` (TLS
  modes including `tlsSkipVerify`, named service vs overall health, metadata,
  the phase metrics the check now reports) and add a changelog entry per
  `wiki/conventions/changelog.md`.
- Extend `GetSampleConfigs` ([samples.go](../../server/internal/checkers/checkgrpc/samples.go))
  with a TLS + named-service sample.

### Tests

- Unit/integration tests against an in-process `grpc-go` health server:
  SERVING, NOT_SERVING-keeps-latency, named service unknown
  (`SERVICE_UNKNOWN` / the friendly NOT_FOUND message), h2c vs TLS vs
  TLS-skip-verify, metadata actually arrives (assert exact keys/values via a
  server interceptor; secret values never appear in `output`), phase metrics
  present and plausible, and phase-classified failures (unresolvable host,
  closed port, TLS against a plaintext server).
- Timing split is real, not cosmetic: assert `rpc_time_ms` no longer
  contains the connect cost — e.g. a server that delays accept vs one that
  delays the RPC handler must move different metrics.
- Keep `tunnel_test.go` green — the dialer changes must not disturb the
  tunneled path; extend it to cover the explicit-connect path.
- dash0 E2E: create a gRPC check with TLS + skip-verify + metadata through
  the form and assert the config round-trips.

### Decisions

- **`keyword` / `invertKeyword`: deprecate, don't expose.** Matching a
  substring of the serving-status enum is redundant with the SERVING check.
  Hide it from the form, keep decoding it for backward compatibility, and
  note it as deprecated in the docs. Confirm nothing in production uses it
  before removing outright.

### Out of scope (note-only)

- mTLS client certificates and a `tlsServerName`/authority override — worth a
  follow-up spec if requested (openstatus doesn't have them either).
- The `Health/Watch` streaming RPC — openstatus doesn't use it either;
  polling `Check` matches our scheduler model.
