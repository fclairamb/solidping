# DNSBL result card — render Spamhaus error/status codes as human-readable labels

## Problem

The backend DNSBL checker was fixed (spec
`specs/done/2026/07/2026-07-10-12-dnsbl-spamhaus-error-return-codes-false-positive.md`)
to classify the reserved `127.255.255.x` replies as **error/status codes** rather
than blocklistings, and it now emits them in a dedicated `error_codes` map in the
result output (`checker.go`, `result.Output["error_codes"]`), alongside
`listed_on`, `clean`, `inconclusive`, and `return_codes`.

But **dash0 has no dedicated rendering for DNSBL results**. There is no
`DnsblCard` component and no `check.type === "dnsbl"` branch in the check detail
page ([`checks.$checkUid.index.tsx`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx),
where `ssl` and `docker` are dispatched around line 1140). A DNSBL result
therefore falls through to the generic raw-JSON `<pre>` dump in the result-detail
route
([`checks.$checkUid.results.$resultUid.tsx:335`](web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx:335)),
so an operator sees a bare, meaningless octet like `127.255.255.254` with no
indication that it means "query was refused because it came from a public
resolver." The existing `dnsbl` locale section
([`en/checks.json:240`](web/dash0/src/locales/en/checks.json:240)) only has
form-field strings — no result-display or error-code labels.

The three codes that must read as plain language:

| Code | Meaning |
|---|---|
| `127.255.255.252` | Typo / malformed DNSBL zone name |
| `127.255.255.254` | Query via public/open resolver — refused |
| `127.255.255.255` | Query volume / rate limit exceeded |

## Proposal

Add a `DnsblCard` and translate the codes.

1. **`DnsblCard` component** under `web/dash0/src/components/checks/dnsbl-card.tsx`,
   mirroring the shape of
   [`ssl-chain-card.tsx`](web/dash0/src/components/checks/ssl-chain-card.tsx) and
   `docker-restart-loop-card.tsx`: takes `output: Record<string, unknown> | undefined`,
   returns `null` when the output carries no DNSBL fields (safe to mount
   unconditionally). It renders:
   - **Zone status** — the `listed_on` zones as destructive badges, `clean` as
     success badges, `inconclusive` as secondary badges.
   - **Error/status codes** — for each zone in `error_codes`, show the zone plus a
     human-readable label per code (see `codeLabel` below), not the raw IP. This
     is the primary ask.
   - **Real listing codes** — for `listed_on` zones, keep showing the raw
     `return_codes` value (the `127.0.0.x` code identifies the sub-list — SBL,
     XBL, PBL — and operators want it verbatim).

2. **`codeLabel(code: string)` helper** mapping reserved codes to i18n keys, with
   a generic fallback so nothing renders as a bare octet:
   - `127.255.255.252` → `checks:dnsbl.errTypo`
   - `127.255.255.254` → `checks:dnsbl.errPublicResolver`
   - `127.255.255.255` → `checks:dnsbl.errRateLimit`
   - any other `127.255.255.x` → `checks:dnsbl.errGeneric` (`"DNSBL error response ({{code}})"`)

3. **Dispatch** — mount `<DnsblCard output={check.lastResult?.output as ...} />`
   under a `check.type === "dnsbl"` branch in
   [`checks.$checkUid.index.tsx`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1140),
   next to the existing `SslChainCard` / `DockerRestartLoopCard` dispatch.
   Consider also rendering it on the single-result detail page so a historical
   result's codes are legible, not just the last result.

4. **i18n** — extend the `dnsbl` section in all four locale files
   (`web/dash0/src/locales/{en,fr,es,de}/checks.json`) with the error-code labels
   and zone-section labels (`dnsbl.title`, `dnsbl.listedOn`, `dnsbl.clean`,
   `dnsbl.inconclusive`, `dnsbl.errorCodes`):

   | Key | en | fr | es | de |
   |---|---|---|---|---|
   | `dnsbl.errTypo` | Typo / malformed DNSBL zone name | Nom de zone DNSBL erroné ou mal formé | Nombre de zona DNSBL erróneo o mal formado | Tipp-/Formfehler im DNSBL-Zonennamen |
   | `dnsbl.errPublicResolver` | Query via public/open resolver — refused | Requête via un résolveur public/ouvert — refusée | Consulta a través de un resolvedor público/abierto — rechazada | Abfrage über öffentlichen/offenen Resolver — abgelehnt |
   | `dnsbl.errRateLimit` | Query volume / rate limit exceeded | Volume de requêtes / limite de débit dépassée | Volumen de consultas / límite de tasa superado | Abfragevolumen / Ratenlimit überschritten |
   | `dnsbl.errGeneric` | DNSBL error response ({{code}}) | Réponse d'erreur DNSBL ({{code}}) | Respuesta de error DNSBL ({{code}}) | DNSBL-Fehlerantwort ({{code}}) |

5. **Design reference** — per the repo convention, if `DnsblCard` introduces a new
   pattern, register it in
   [`design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx);
   otherwise it should compose from existing `Card` / `Badge` / `Table`
   primitives already catalogued there. Card must be mobile-usable (wrapping
   badges, no fixed widths).

6. **status0 (public status page)** — if `web/status0` surfaces DNSBL result
   detail to subscribers, apply the same label mapping there; confirm during
   implementation whether it renders check output at all. Likely no change.

7. **Tests** — Playwright/component test asserting the card renders the
   human-readable label (e.g. "Query via public/open resolver — refused") and
   **not** the raw `127.255.255.254`, for a check whose last result carries an
   `error_codes` entry. Add a no-regression case where a real `127.0.0.2` listing
   still shows as a destructive `listed_on` badge with the raw code.

## Notes

- Backend already provides everything the card needs; this is a pure dash0 change.
- Real-world trigger: prod check `api.acme.io (dnsbl)` sits Down on Spamhaus
  returning `127.255.255.254` from an AWS resolver — a false positive that this
  card will make self-explanatory once the backend fix is deployed.

## Implementation Plan

Pure dash0 frontend change (backend already emits `error_codes` /
`return_codes`). Backend output keys are snake_case and preserved verbatim in
`result.output`: `listed_on`/`clean`/`inconclusive` are `string[]` zone lists,
`error_codes`/`return_codes` are `Record<zone, string[]>` code maps.

1. **`DnsblCard` component** — new `web/dash0/src/components/checks/dnsbl-card.tsx`,
   mirroring `ssl-chain-card.tsx`. Signature `({ output }: { output: Record<string, unknown> | undefined })`.
   Returns `null` unless the output carries the DNSBL signature (`listed_on`,
   `clean`, `inconclusive` all arrays) so it is safe to mount unconditionally.
   Renders three sections inside a `Card`:
   - **Zone status** — `listed_on` zones as `destructive` badges (with their raw
     `return_codes` value appended verbatim, e.g. `spamhaus.org (127.0.0.2)`),
     `clean` as `success` badges, `inconclusive` as `secondary` badges.
   - **Diagnostic responses** — for each zone in `error_codes`, the zone plus a
     human-readable `codeLabel` per code (never the raw octet).
   - Mobile-friendly: wrapping `flex flex-wrap gap-2`, no fixed widths.
2. **`codeLabel(t, code)` helper** — maps the three reserved codes to
   `checks:dnsbl.errTypo` / `errPublicResolver` / `errRateLimit`, with any other
   `127.255.255.x` (and unknown) falling back to `checks:dnsbl.errGeneric`
   (`"DNSBL error response ({{code}})"`), so nothing renders as a bare octet.
3. **Dispatch** — mount `<DnsblCard output={...} />` under a `check.type === "dnsbl"`
   branch in `checks.$checkUid.index.tsx` beside `SslChainCard`/`DockerRestartLoopCard`,
   and also unconditionally on the single-result detail page
   (`checks.$checkUid.results.$resultUid.tsx`) so historical results are legible;
   strip the DNSBL display keys from that page's raw-JSON dump to avoid dupes.
4. **i18n** — extend the `dnsbl` section in `en/fr/es/de/checks.json` with the
   four error-code labels plus section labels (`dnsbl.title`, `dnsbl.listedOn`,
   `dnsbl.clean`, `dnsbl.inconclusive`, `dnsbl.errorCodes`).
5. **Design reference** — `DnsblCard` composes only from already-catalogued
   `Card`/`Badge` primitives (like the SSL/Docker cards, which are not registered
   there either), so no `design-reference.tsx` change is needed.
6. **status0** — confirm it does not surface DNSBL result detail; expected no change.
7. **Tests** — Playwright E2E (`e2e/dnsbl-card.spec.ts`) mocking the single-result
   endpoint with an `error_codes` output: asserts the human-readable label renders
   and the raw `127.255.255.254` does not, plus a no-regression case where a real
   `127.0.0.2` listing shows as a `destructive` `listed_on` badge with the raw code.
