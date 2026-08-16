# DNS check form is broken: samples load empty, config key mismatch, no nameserver field

## Problem

Reported on `https://solidping.k8xp.com/dash0/orgs/acmetech/checks/new?checkType=dns`:
loading a DNS sample ("Load sample…" → e.g. *Google DNS A Record*) fills the
name/slug/interval but leaves the **Domain** field empty, so the sample
produces an invalid check. Digging in, the DNS form has three related defects:

1. **Samples don't populate the Domain field.** The backend DNS samples
   (`server/internal/checkers/checkdns/samples.go:15`) serialize through
   `DNSConfig.GetConfig()` (`server/internal/checkers/checkdns/config.go:121`),
   which emits the domain under the `host` key. But `applySample` in
   `web/dash0/src/components/shared/check-form.tsx:627` populates the DNS
   form's Domain input from the `domain` key — which no DNS sample ever
   contains. `setHost` receives the value instead (line 625), and the DNS
   form doesn't render a host field.

2. **The form submits a config key the backend doesn't read.** For
   `type === "dns"` the form builds `{ domain: … }`
   (`web/dash0/src/components/shared/check-form.tsx:759-762`, a case shared
   with the `domain`-expiration type, which legitimately uses that key —
   `server/internal/checkers/checkdomain/config.go:14`). The DNS backend
   config only reads `url`, `host` (legacy alias `hostname`), `nameserver`,
   `record_type`, `timeout`, `expected_ips`, `expected_values`
   (`server/internal/checkers/checkdns/config.go:24-69`) and `Validate`
   rejects an empty host (`server/internal/checkers/checkdns/checker.go:59`).
   The only server-side use of `domain` is name auto-generation
   (`server/internal/handlers/checks/handler.go:233`). So a manually-filled
   DNS check from this form fails with `host is required` — and since the
   error comes back keyed `host` while the form looks up field errors under
   `domain` (`check-form.tsx:1680-1681`), the message likely doesn't even
   render next to the field.

3. **No way to specify a DNS server.** The backend supports an optional
   `nameserver` (resolver) but the DNS form renders only the Domain input
   (`web/dash0/src/components/shared/check-form.tsx:1674-1683`). The user
   expectation: a DNS check specifies a domain and *optionally* a server to
   query.

## Proposal

Frontend (`web/dash0/src/components/shared/check-form.tsx`):

- Split the `dns` case from the `domain` case in `currentConfig`: for DNS,
  submit `{ host: domain }` (keep the visible label "Domain" — that's the
  right word for the thing being queried) plus `nameserver` when filled.
- In `applySample`, keep populating the shared `domain` state but source it
  per-type: for DNS samples read `host`, and also populate the new nameserver
  state from `nameserver`. (Alternatively reuse the existing `host` state for
  the DNS field — whichever keeps the shared-state plumbing simplest.)
- Add an optional **DNS server** input for the `dns` type (placeholder e.g.
  `8.8.8.8 — defaults to system resolver`), wired to `nameserver`. Follow the
  design reference for the optional-field pattern.
- Map validation errors for DNS from the backend's `host` field onto the
  Domain input so the error renders inline.
- When editing an existing DNS check, initialize the form state from `host`
  (and `nameserver`) as well — check the `initialData` hydration paths near
  `check-form.tsx:474-483` for the pattern.

Samples (`server/internal/checkers/checkdns/samples.go`):

- Keep at least one plain sample (system resolver) and add/adjust one that
  sets an explicit `Nameserver` (e.g. query `google.com` A via `8.8.8.8`),
  so the optional-server path is exercised and discoverable from the picker.

Tests:

- Playwright: load a DNS sample → Domain field is filled → submit succeeds;
  create a DNS check with an explicit server; edit round-trip preserves both
  fields.
- Backend samples already run through `startup_samples_test.go` — verify any
  sample changes keep it green.

Open questions:

- Should the DNS form also expose `record_type` (samples set it to `A`;
  today the value silently drops when saving from the form)? Probably yes as
  a select, but it can be a follow-up — the sample's record type shouldn't
  be silently discarded either way.
- Audit whether other check types' samples have the same emit-key vs
  form-key mismatch (e.g. types whose backend `GetConfig()` emits keys
  `applySample` never reads); the DNS fix pattern should be applied
  wherever that occurs.

## Implementation Plan

Root cause confirmed by reading the code:
- Backend DNS config emits/reads the domain under `host` (with legacy alias
  `hostname`); `GetConfig()` never emits `domain`
  (`server/internal/checkers/checkdns/config.go`).
- The dash0 form's `dns` case was folded together with the `domain`-expiration
  case in three places (`applySample`, the `currentConfig` validation builder,
  the submit-time builder, and the field render), all keyed on `domain`. So
  samples (which carry `host`) never filled the field, the form submitted
  `domain` (which the DNS backend ignores → `host is required`), and the
  inline error keyed `host` never rendered next to a field looked up under
  `domain`.
- Nameserver validation is strict `host:port` (`checker.go:82`), so the sample
  and placeholder must use `8.8.8.8:53`, not a bare IP.

Steps:

1. **Backend samples** (`checkdns/samples.go`): keep the three plain
   system-resolver samples and add a fourth, "Google DNS A via 8.8.8.8" (slug
   `dns-google-8888`), setting `Nameserver: "8.8.8.8:53"` so the optional-server
   path is exercised and discoverable. Add `checkdns/samples_test.go` that
   round-trips every sample through `FromMap`/`Validate`/`GetConfig` (mirrors
   `checkssl/samples_test.go`), asserting the nameserver survives. Keeps
   `startup_samples_test.go` green (DNS count stays ≥ 3, `dns-google` slug
   preserved).

2. **Frontend — split `dns` from `domain`** in `check-form.tsx`. The DNS field
   reuses the existing shared `host` state (simplest plumbing: `applySample`
   already sets `host` from `cfg.host`, edit hydration already inits `host`
   from `config.host`, and the backend's `host`-keyed validation error renders
   automatically). Add two DNS-only states: `dnsNameserver`
   (init/apply/submit under `nameserver`) and `dnsRecordType`
   (init/apply/submit under `record_type`, default `A`, so a sample's record
   type is no longer silently discarded).
   - `currentConfig` (validation builder): `case "dns"` → `cfg.host = host`,
     `cfg.nameserver` when set, `cfg.record_type` when non-default.
   - submit builder: `case "dns"` → require `host` ("Domain is required"),
     `config.host`, `config.nameserver`, `config.record_type`.
   - render: split `case "dns"` from `case "domain"`. DNS renders the Domain
     input (bound to `host`, error under `host`), an optional **DNS server**
     input (placeholder `8.8.8.8:53 — defaults to system resolver`, error under
     `nameserver`), and a **Record type** select (A/AAAA/CNAME/MX/NS/TXT).
   - add `dnsNameserver`/`dnsRecordType` to the `currentConfig` deps.

3. **E2E** (`web/dash0/e2e/checks.spec.ts` or a new `dns-check.spec.ts`):
   load a DNS sample → Domain field filled → submit succeeds; create a DNS
   check with an explicit server; edit round-trip preserves domain + server.

4. **QA**: `make build-backend lint-back test`, `make build-dash0`, dash0
   `bun run lint` (no new errors), run the DNS e2e file if the local devloop
   is in test mode (author it regardless).

Out of scope / follow-up: broader audit of other check types' emit-key vs
form-key mismatches (noted as an open question); the record-type select is
included here rather than deferred since it directly resolves the
silent-discard concern.
