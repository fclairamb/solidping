# IMAP/POP3: default implicit TLS from the well-known port (and mirror it in the check form)

## Context

The 2026-07-04/05 dev incident started with a user-created check
`{host: outlook.office365.com, port: 993}` — port 993 **is** the
IMAPS (implicit TLS) port, but the `tls` flag defaults to false, so the
checker speaks plaintext to a TLS-only endpoint. Today that hangs the fleet
(fixed by specs `2026-07-05-04`/`05`); after those fixes it still yields a
misleading eternal `timeout` for a mailbox that is perfectly up. The same
trap exists for POP3 `:995`. Nothing in the API or the dashboard form pushes
the user toward the only sensible interpretation of those ports.

`checksmtp` already solved this: it *derives* implicit TLS from the port —
`useImplicitTLS := params.port == implicitTLSPort && !cfg.StartTLS`
(`server/internal/checkers/checksmtp/checker.go:120`, port 465). IMAP/POP3
only map the flag→port direction (`tls: true` defaults the port to 993/995 in
`newExecParams`) and never port→flag.

## Current state (verified 2026-07-05; re-verify at build)

- `checkimap`: `implicitTLSPort = 993` (`config.go:13`), `TLS`/`StartTLS`
  bools, `Validate()` rejects `tls && starttls` (`config.go:165`).
- `checkpop3`: `implicitTLSPort = 995` (`config.go:13`), same shape.
- `Checker.Validate(spec)` runs at check create/update and already mutates
  the spec (defaults `Name`/`Slug` — `checkimap/checker.go:44-51`), so
  config normalization at write time is an established pattern.
- Dashboard check form: IMAP/POP3 config editors expose host/port/tls
  toggles (`web/dash0/src/…` — locate the per-type config forms at build;
  design reference at `/orgs/$org/design-reference`).

## Design decisions

### D1 — Execution derives implicit TLS from the port, like SMTP

In `checkimap`/`checkpop3` `newExecParams` (or `Execute` pre-amble): when
`port == implicitTLSPort` and neither `tls` nor `starttls` is set, run with
implicit TLS. Execution-time derivation (not a stored-config rewrite) matches
the `checksmtp` precedent, fixes **existing** stored checks (the webingenia
ones start working on deploy without an edit), and keeps PATCH semantics
untouched.

An explicit `tls: false` cannot be distinguished from "unset" with the
current `bool` fields — acceptable: plaintext IMAP/POP3 on 993/995 is not a
real configuration (the port's sole meaning is implicit TLS). Someone probing
odd server behavior can use a non-standard port or the `tcp` checker.

### D2 — The form makes the coupling visible

In the dashboard IMAP/POP3 check forms: selecting port 993/995 (or leaving
port empty while toggling TLS) auto-enables the TLS toggle with a short hint
("Port 993 uses implicit TLS"); switching TLS off restores the plaintext
default port (143/110) in the placeholder. Purely client-side affordance —
the server-side derivation (D1) remains the source of truth. Follow the
design-reference form patterns; keep it usable on mobile.

### D3 — Samples and docs say `tls: true` explicitly

`checkimap/samples.go` / `checkpop3` samples and the docs site examples for
mail checks always spell out `tls: true` with port 993/995 so copy-pasted
configs are unambiguous, independent of D1.

## Implementation

1. `checkimap`: derive implicit TLS per D1 in `newExecParams`; keep the
   `tls && starttls` validation. Same for `checkpop3`.
2. Unit tests: `{port: 993}` → TLS dial (assert via a local TLS listener);
   `{port: 993, starttls: true}` → plaintext dial + STARTTLS (explicit
   starttls wins); `{port: 143}` → plaintext; POP3 mirror cases for 995/110.
3. dash0: port↔TLS auto-toggle + hint in the IMAP and POP3 config forms;
   update/extend the relevant form component tests or e2e happy path.
4. Update samples + docs examples (D3).

## Out of scope

- Socket deadlines (spec `2026-07-05-04`) and the runner watchdog
  (spec `2026-07-05-05`).
- FTPS (`:990`), LDAPS, or a general port→TLS registry across checkers —
  revisit if this trap recurs elsewhere.
- Converting `tls`/`starttls` to tri-state pointers to honor an explicit
  `false` on 993/995.

## Verification

- `make test`, `make lint`, `make test-dash` (or the scoped form e2e).
- Manual: create `{host: outlook.office365.com, port: 993}` with no TLS flag
  → check goes `up` with `tls_version`/`tls_cipher` in the result output.

## Key files

- `server/internal/checkers/checkimap/{checker,config,samples}.go`
- `server/internal/checkers/checkpop3/{checker,config}.go`
- `server/internal/checkers/checksmtp/checker.go:120` (precedent, unchanged)
- `web/dash0/src/` per-type check config forms (IMAP/POP3)

## Risk log

- **Behavior change for stored checks**: any existing `{port: 993, tls
  unset}` check flips from timeout/hang to a real TLS probe — that is the
  point; no plausible user depends on the broken behavior.
- **D1 vs explicit false**: documented limitation (see D1); the escape hatch
  is a non-standard port or the `tcp` checker.

## Implementation Plan

1. **`checkimap` execution-time derivation** (`server/internal/checkers/checkimap/checker.go`):
   - Add a `useImplicitTLS bool` field to the existing `execParams` struct.
   - In `newExecParams`, after the port default is resolved, set
     `params.useImplicitTLS = cfg.TLS || (params.port == implicitTLSPort && !cfg.StartTLS)`.
     This preserves an explicit `tls: true` at any port, adds the port-993 default
     only when neither `tls` nor `starttls` is set, and lets an explicit
     `starttls: true` win (mirrors `checksmtp/checker.go:120`'s
     `useImplicitTLS := params.port == implicitTLSPort && !cfg.StartTLS`, generalized
     to also respect a pre-existing `cfg.TLS` since IMAP/POP3, unlike SMTP, already
     let `tls` drive the port default the other way in `newExecParams`).
   - Replace the three `cfg.TLS` usages inside `Execute` (the `dial()` call's
     `implicitTLS` arg, and the two `if cfg.TLS` blocks — one for the immediate
     post-dial TLS output, one is actually the same block) with
     `params.useImplicitTLS`.
   - Leave `Validate()`'s `tls && starttls` rejection untouched — it's still a
     legitimate stored-config error.
2. **`checkpop3` execution-time derivation** (`server/internal/checkers/checkpop3/checker.go`):
   mirror step 1 exactly (`implicitTLSPort` = 995, same `execParams`/`newExecParams`/
   `Execute` shape).
3. **Unit tests** (both `checker_test.go`s), added as new subtests inside the
   existing `TestIMAPChecker_Execute` / `TestPOP3Checker_Execute` table plus one
   dedicated test each for the real TLS dial (table-driven subtests can't easily
   spin up a TLS listener with a different opts shape):
   - `TestIMAPChecker_Execute_ImplicitTLSFromPort` (and POP3 mirror): start a
     **local TLS listener** (self-signed cert, `tls.Listen` wrapping the existing
     fake-server accept loop) on a port, build `IMAPConfig{Host, Port: <that port>}`
     with neither `TLS` nor `StartTLS` set (but tolerate the ephemeral port not
     literally being 993/995 — see note below), call `Execute`, assert
     `StatusUp` and `output["tls_version"]`/`output["tls_cipher"]` present.
     Since `httptest`-style ephemeral ports can't literally be 993/995 without
     root/collision risk, this test must directly construct `execParams` (or
     call `newExecParams` with a `cfg.Port` forced to `implicitTLSPort` via a
     **loopback port-forward is overkill** — instead, test `newExecParams`
     directly as a unit test (no real dial): `newExecParams(&IMAPConfig{Port:
     implicitTLSPort}).useImplicitTLS == true`. Reserve the full `Execute`
     integration TLS-listener test for the case where `Port` is left at 0 and
     `TLS: true` is explicit (already representable at any ephemeral port),
     which exercises the *combined* codepath (`useImplicitTLS` computed, then
     consumed by `dial`/output). This still gives full coverage of both halves
     (derivation logic in `newExecParams`, consumption in `Execute`) without
     fighting privileged ports.
   - `newExecParams` unit tests (table-driven, no network): `{port: 993}` →
     `useImplicitTLS: true`; `{port: 993, starttls: true}` → `useImplicitTLS:
     false` (explicit starttls wins); `{port: 143}` → `useImplicitTLS: false`;
     `{tls: true, port: 143}` → `useImplicitTLS: true` (explicit tls preserved
     at a non-standard port). POP3 mirror with 995/110.
   - `Execute`-level integration test reusing `startFakeIMAPServer`/
     `startFakePOP3Server`: add a `tls bool` opt to wrap the fake listener with
     `tls.Listen` + a generated self-signed cert when true, matching the
     STARTTLS test's "not exercised here" comment style but actually completing
     the handshake this time. Config: `Port: <fake TLS listener port>, TLS:
     true` (explicit, standard way to reach an arbitrary-port TLS listener in
     tests) to prove the `useImplicitTLS` consumption path (dial + tls_version
     output) works; the port-993-specific *trigger* is covered by the
     `newExecParams` unit test above, so together they cover the full spec
     requirement without needing a literal port-993 bind in CI.
   - `{port: 143}` / `{port: 110}` plain listener case: already covered by the
     existing "successful basic connection" subtest (port is ephemeral, TLS/
     STARTTLS both unset) — confirms unchanged plaintext behavior, no new test
     needed beyond asserting it still passes.
4. **dash0 form affordance** (exact files TBD at explore time — locate via the
   design reference and the per-check-type config form directory, likely
   `web/dash0/src/components/checks/config/` or
   `web/dash0/src/routes/orgs/$org/checks/...`): in the IMAP and POP3 config
   forms, wire a derived-state effect (or inline handler) so that:
   - Setting port to `993`/`995` (IMAP/POP3 respectively) auto-checks the TLS
     toggle and shows a short inline hint ("Port 993 uses implicit TLS") using
     whatever hint/description primitive the design reference documents for
     form fields.
   - Unchecking TLS afterwards restores the plaintext placeholder (`143`/`110`)
     for the port field (placeholder only, not a forced value, so it doesn't
     fight a user-entered port).
   - This is presentation-only; it must not change what gets submitted beyond
     the user's own toggle state — the server derives independently (D1) so a
     stale/bypassed client can't produce a wrong result, only a confusing UI.
   - Extend or add a component test (or e2e happy-path test under
     `web/dash0/e2e/`) exercising: type 993 into port → TLS toggle becomes
     checked + hint visible; toggle TLS off → port placeholder reverts.
5. **Samples/docs** (D3): `checkimap/samples.go`'s existing "Gmail IMAP" sample
   already sets `TLS: true` explicitly alongside `Port: implicitTLSPort` — verify
   only, no change needed. `checkpop3` has no `samples.go` (doesn't implement
   `CheckerSamplesProvider` — out of scope to add one, per the spec's own
   "Out of scope" list implying no new surfaces beyond what's named). Docs:
   `web/docs/docs/features/check-types.md`'s IMAP/POP3 sections currently show
   only bare URL forms (`imaps://hostname:993  # With SSL`,
   `pop3s://hostname:995  # With SSL`) with no JSON config sample — add a short
   explicit note/line under each (matching the table style already used) making
   clear that the JSON check config should set `"tls": true` alongside port
   993/995, so a reader translating the URL form into a JSON check body doesn't
   drop the flag.
6. Run `make fmt`, then the QA matrix (`make build-backend lint-back test`,
   `make build-dash0` + `bun run lint` in `web/dash0/`, `make build-docs`).
7. Self-review against D1/D2/D3 before merge, in particular: explicit
   `starttls: true` on port 993/995 must still win over the port-derived
   default (verified by the `newExecParams` unit test in step 3).
