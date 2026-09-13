---
model: opus
effort: high
---

# Saving an HTTP check in dash0 silently deletes every config key the form does not model — request `body` and `headers` included

## Problem

An HTTP check created through the API with a request body was broken by
opening it in the dashboard and pressing **Save** without changing anything.

Concrete case (2026-09-11, solidping.k8xp.com, org `stonal`, check
`sso-keycloak-login`): a `POST` to a Keycloak token endpoint with

```json
{
  "url": "https://sso.acme.com/realms/acme/protocol/openid-connect/token",
  "method": "POST",
  "headers": { "content-type": "application/x-www-form-urlencoded" },
  "body": "grant_type=password&client_id=admin-cli&scope=openid&username=…&password=…",
  "expected_status": 200,
  "followRedirects": false,
  "timeout": "15s",
  "json_path_assertions": { "type": "and", "children": [ … ] }
}
```

After one UI save the stored config was

```json
{ "url": …, "method": "POST", "followRedirects": false, "timeout": "15s",
  "jsonPathAssertions": { … } }
```

`body`, `headers` and `expected_status` were gone. The probe now sends a
body-less `POST`, Keycloak answers `400 invalid_request`, the check flips to
`down`, and nothing in the UI ever said a field was being discarded — the form
never showed those fields, so the user had no way to know they existed, let
alone that Save would delete them.

Reported in the field the same day, in the `#solidping-dev` thread on the
incident this check raised
(https://stonaltech.slack.com/archives/C0BFC2XG4K0/p1789139320451959?thread_ts=1789138055.738169&cid=C0BFC2XG4K0):
« le check de type `http` … l'UI ne permet pas de passer un body donc si on
l'édite on le casse ». The sibling specs filed from that thread are
`2026-09-11-02` (export leaks), `-03` (secret references), `-04`
(config-as-code round-trip) and `-05` (the `js` check's `http` helper).

### Root cause

Two designs that are each correct in isolation compose into data loss:

1. **The type module rebuilds `config` from its own state on every save.**
   `httpModule.toConfig`
   ([web/dash0/src/components/checks/form/types/http.tsx:143](web/dash0/src/components/checks/form/types/http.tsx:143))
   starts from `const cfg: CheckConfig = {}` and writes only the keys
   `HttpState` models: `url`, `method`, `expectedStatusCodes`, `username` /
   `password` / `basicAuth`, `secretHeaders`, `verifySsl`, `followRedirects`,
   `capture_failure_response`, `jsonPathAssertions`. The shared form then adds
   `timeout` on top
   ([web/dash0/src/components/shared/check-form.tsx:742-745](web/dash0/src/components/shared/check-form.tsx:742)),
   which is the only reason `timeout` survives. Nothing carries
   `initialData.config` through.

2. **The server's PATCH-merge uses replace semantics for public keys.**
   `mergePatchConfig`
   ([server/internal/handlers/checks/service.go:4726-4745](server/internal/handlers/checks/service.go:4726))
   preserves *secret* keys that are absent from the patch but, for every
   non-secret key, "the patch wins fully" — a public key missing from the
   submitted config is dropped from the stored one. `toConfig` knows this and
   depends on it: the comment at
   [http.tsx:184-190](web/dash0/src/components/checks/form/types/http.tsx:184)
   says omitting `jsonPathAssertions` "is itself what clears a stored value".

So the module's *intentional* omit-to-clear for the keys it owns is
indistinguishable, at the server, from *accidental* omission of keys it has
never heard of. Every public key the form does not model is deleted on save.

### What the HTTP form does not model

The backend accepts all of these
([server/internal/checkers/checkhttp/config.go:61-125](server/internal/checkers/checkhttp/config.go:61)),
the form models none of them, and each is silently destroyed by a UI save:

| key | purpose |
|---|---|
| `body` | request body — required for any `POST`/`PUT`/`PATCH` probe |
| `headers` | plain (non-secret) request headers, e.g. `content-type`, `accept` |
| `body_expect` / `body_reject` | substring assertions on the response body |
| `body_pattern` / `body_pattern_reject` | regex assertions on the response body |
| `headers_pattern` | regex assertions on response headers |
| `expected_status` (snake) | the server accepts both spellings via `resolveKey` ([config.go:164](server/internal/checkers/checkhttp/config.go:164)); `seedExpectedStatusCodes` ([http.tsx:75-89](web/dash0/src/components/checks/form/types/http.tsx:75)) reads only `expectedStatusCodes` / `expectedStatus`, so a snake-spelled value seeds the default `["200"]`, which `toConfig` then omits as implicit |

A `POST` check with a body is not exotic — it is how you monitor a login, a
GraphQL endpoint, a webhook receiver, or any JSON API that is not `GET`-only.
Today it can be created via API, manifest, CLI or MCP, and cannot be *touched*
in the UI without being broken.

### This is the third time

The same mechanism has shipped the same bug twice before, each time fixed by
adding one more field to `HttpState`:

- **Secret headers** were wiped on every edit until the dirty-flag scheme was
  added — see the `HttpState` comment at
  [http.tsx:53-58](web/dash0/src/components/checks/form/types/http.tsx:53)
  ("Sending them anyway is what used to wipe secret headers on every edit")
  and spec `2026-05-18-07-secret-http-headers`.
- **JSONPath assertions** were destroyed on save until spec
  `2026-08-28-12-http-json-path-assertions-ui` added the field; that spec's own
  e2e header
  ([web/dash0/e2e/check-http-json-assertions.spec.ts:2-8](web/dash0/e2e/check-http-json-assertions.spec.ts:2))
  names the cause: "toConfig rebuilt the config from scratch, so saving an HTTP
  check with assertions from the UI silently destroyed them".
- **Request body and headers** — this spec.

Adding `body` and `headers` as fields fixes the reported case and leaves
`body_expect`, `body_pattern`, `headers_pattern` and the next backend key to be
the fourth, fifth and sixth occurrence. The fix has to be structural.

### Also observed: period reset

On the same save the check's `period` went from `00:05:00` to `00:01:00`.
Reading the current source does not explain it: `00:05:00` is a ladder step
([check-form.tsx:231](web/dash0/src/components/shared/check-form.tsx:231)), the
form seeds `period` from `initialData.period`
([check-form.tsx:544-545](web/dash0/src/components/shared/check-form.tsx:544)),
and non-ladder values get a custom entry
([check-form.tsx:651-663](web/dash0/src/components/shared/check-form.tsx:651),
spec `2026-08-26-05`). The instance was `0.27.1` (`81c9f84`). Treat it as
unconfirmed: the e2e below must assert period round-trip for an API-created
check; if it reproduces, fix it in this spec, otherwise the test is the proof
that it does not.

## Proposal

Three parts. (1) is the structural fix and the reason for the `opus`/`high`
tag; (2) is the user-facing feature gap; (3) is the proof.

### 1. Pass unmodeled public keys through, per module, by declaration

Make "a key the module does not model" and "a key the module deliberately
cleared" distinguishable, at the form layer, so the server's replace
semantics can stay as they are (they are load-bearing for omit-to-clear, and
the server cannot tell the two apart — only the form can).

- Extend `CheckTypeModule`
  ([web/dash0/src/components/checks/form/types/index.ts](web/dash0/src/components/checks/form/types/index.ts))
  with a required `ownedKeys: readonly string[]` — every config key the
  module reads in `fromConfig` or writes in `toConfig`, **in every spelling
  the module or the server accepts** (`expectedStatus`, `expected_status`,
  `expectedStatusCodes`, `expected_status_codes`, `jsonPathAssertions`,
  `json_path_assertions`, `capture_failure_response`,
  `captureFailureResponse`, …). Omitting a key from `ownedKeys` means "I do
  not model this; preserve it".
- In the shared form, where the submitted config is assembled
  ([check-form.tsx:742](web/dash0/src/components/shared/check-form.tsx:742)),
  build it as: **every key of `initialData.config` that is neither in the
  active module's `ownedKeys` nor a shared-form key (`timeout`) nor a server
  secret field**, then layer `serialized.config` and `timeout` on top. In
  create mode `initialData` is empty and this is a no-op; when a sample is
  applied, passthrough comes from the sample's config the same way.
- Secret fields — for HTTP `basicAuth`, `password`, `secretHeaders`
  ([config.go:519-521](server/internal/checkers/checkhttp/config.go:519)) —
  never come back on GET, so they cannot be in `initialData.config`; exclude
  them explicitly anyway so a future plaintext-fallback deployment (which
  *does* return them — see the `authDirty` comment at
  [http.tsx:135-138](web/dash0/src/components/checks/form/types/http.tsx:135))
  cannot make passthrough re-send a credential the user did not touch and
  defeat the dirty-flag contract. The list of secret fields per type should
  come from one place the frontend already knows (the check-types metadata the
  form loads, or a small mirror in `common.ts` if the API does not expose it —
  implementer's call, but it must not be a second hand-maintained copy inside
  `http.tsx`).
- **Type switch in create mode**: passthrough must be keyed to the *active*
  module. Switching type on the new-check page already resets config; make
  sure passthrough does not smuggle an `http`-only key into a `tcp` payload
  (`initialData` is undefined in create mode, so this should fall out
  naturally — add the test anyway).
- **Every module declares `ownedKeys`.** This is the cost of the structural
  fix and the reason it is not a one-file change: `database.tsx`, `dns.tsx`,
  `game.tsx`, `grpc`, `infra.tsx`, `mail.tsx`, `messaging.tsx`, `misc.tsx`,
  `network.tsx`, `web.tsx`, `clickhouse.tsx`. A module that under-declares
  resurrects a key it meant to clear (omit-to-clear breaks for that key); a
  module that over-declares is today's behaviour for that key. The type
  system should make the field required so a new module cannot forget it.
  For each module, derive the list from its `fromConfig`/`toConfig` and the
  backend `FromMap` for that type; where the backend accepts an alias via
  `resolveKey`, include both spellings.
- The live preview uses the same `serialized` config
  ([common.ts:5-7](web/dash0/src/components/checks/form/types/common.ts:5)
  — "the SINGLE source for both the live preview and the submitted payload");
  the passthrough must feed both so the preview shows what will actually be
  saved.

### 2. Model request `body` and `headers` in the HTTP form

Passthrough stops the bleeding; users still need to *see and edit* a
`POST` body. In the HTTP **Advanced** section (next to the JSONPath editor —
[http.tsx:~490](web/dash0/src/components/checks/form/types/http.tsx:490)):

- **Request body**: a `Textarea`, shown for any method other than `GET` /
  `HEAD` (hidden-but-preserved otherwise — switching to `GET` must not delete
  a body the user may switch back to; write it whenever non-empty). Written
  as `body`.
- **Request headers**: the same key/value row editor `secretHeaders` uses
  ([http.tsx:297-](web/dash0/src/components/checks/form/types/http.tsx:297)),
  labelled to make the distinction explicit ("Headers" vs "Secret headers —
  encrypted, never shown again"), written as `headers`. Plain headers *do*
  round-trip on GET, so no dirty flag: an empty editor omits the key, which
  clears it — same rule as `jsonPathAssertions`.
- Read `expected_status` (snake) in `seedExpectedStatusCodes` as a third
  fallback so a snake-spelled API value seeds the chip instead of the
  default. `toConfig` keeps writing `expectedStatusCodes` only.
- `body_expect` / `body_reject` / `body_pattern` / `body_pattern_reject` /
  `headers_pattern`: **not** modelled by this spec — covered by passthrough,
  which is the point. If the implementer finds them cheap to add as fields,
  fine, but do not let that widen the change; list them in `ownedKeys` only
  if actually modelled.
- Start from the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) for the textarea
  and key/value editor; if the key/value row editor is not there as a
  primitive yet, add it. Mobile: the row editor must not need horizontal
  scroll at 375px — stack key over value on narrow widths.
- Locale keys for every new string in **all four** locales
  (`web/dash0/src/locales/{de,en,es,fr}/checks.json`); `locale-parity.test.ts`
  guards it.

### 3. Tests that prove the negative

The bug is "nothing visibly happens and data is gone", so every test here
must assert on the **stored** config read back from the API after a real
save, never on client state.

**Unit (`web/dash0/src/components/checks/form/types/http.test.ts`, `bun run test:unit`):**

- `toConfig(fromConfig(c))` for a config carrying `body`, `headers`,
  `body_expect`, `headers_pattern` and a snake `expected_status` — assert the
  passthrough layer (test it through whatever helper the shared form uses, so
  the test exercises the real assembly, not a re-implementation) keeps the
  unmodelled keys byte-for-byte and seeds `expected_status` into the chip.
- Owned-key omission still clears: a config with `jsonPathAssertions` whose
  state has it set to `null` produces a config **without** the key (the
  passthrough must not resurrect it).
- Secret fields are never passed through even when present in the input.
- A small table test over **every** registered module: `ownedKeys` is
  non-empty, and every key `toConfig` writes for a fully-populated state is
  in `ownedKeys` (catches under-declaration mechanically).

**E2E (`web/dash0/e2e/check-http-request-body-headers.spec.ts`, modelled on
`check-http-json-assertions.spec.ts`):**

- API-create an `http` check in org `test` with `method: POST`, a `body`, a
  `headers` map, snake `expected_status: 200`, `body_expect`, a
  `headers_pattern`, and `period: "00:05:00"`. Open `/checks/$uid/edit`,
  click Save **without touching any field**, then `GET` the check and assert:
  `body`, `headers`, `body_expect`, `headers_pattern` unchanged; the status
  chip still means 200 (either spelling the server re-emits is acceptable —
  assert on the effective value, not the key name); `period` still
  `00:05:00`. This is the reported bug and the period observation in one
  test; it must fail on `main` today.
- Edit the body and add a header through the new UI, save, reload the edit
  page: both shown; `GET` confirms.
- Clear the headers editor, save: `headers` key absent from `GET` (omit-to-
  clear still works for a modelled key).
- Switch method to `GET` with a body set, save, switch back to `POST`: body
  still there.

Run the **full** dash0 e2e suite before calling it done, not just the new
file — the shared-form change touches every type's save path.

### Out of scope

- Changing the server's `mergePatchConfig` semantics. Omit-to-clear on public
  keys is relied on by every module and by the manifest apply path; the
  form is the only layer that can tell owned from unknown.
- The `js` check's `http` helper (no cookie jar / cannot disable redirects)
  and body-level secret references (`${env:}` for `body`) — both real, both
  separate specs.

## Implementation Plan

### Step 1 — `secretFields` in the check-types metadata (backend)

The frontend needs an authoritative, non-drifting list of the secret config keys
per check type. `registry.ParseConfig(type)` + `credentials.SecretFieldsFor(cfg)`
already produce it server-side (it is what `redactSecretConfig` and
`applyConfigPatch` use), so expose it rather than hand-maintaining a mirror:

- `internal/handlers/checktypes/service.go`: add `SecretFields []string
  \`json:"secretFields"\`` to `CheckTypeResponse`, filled in `toResponse` from
  `credentials.SecretFieldsFor(registry.ParseConfig(type))`, sorted for stable
  output. Always a non-nil slice so the JSON is `[]`, never `null`.
- Go test asserting `http` advertises `basicAuth`/`password`/`secretHeaders`
  and that a no-secret type (e.g. `icmp`) advertises `[]`.
- `openapi.yaml`: document the new field.

### Step 2 — `ownedKeys` on every `CheckTypeModule`

- `types/index.ts`: add **required** `ownedKeys: readonly string[]` to
  `CheckTypeModule`, so a new module cannot forget it (type error).
- Fill it in for every module from its `fromConfig`/`toConfig` plus the
  backend's `resolveKey` aliases (only `checkhttp` has any). `heartbeat` and
  `email` model no config keys at all → `[]`, which is exactly what makes
  passthrough preserve their public `token`.

### Step 3 — the passthrough assembly helper (`types/common.ts`)

`assembleSubmittedConfig({ initialConfig, ownedKeys, secretFields, moduleConfig,
sharedConfig })` = `{ ...passthrough, ...moduleConfig, ...sharedConfig }` where
`passthrough` is every key of `initialConfig` that is **not** in `ownedKeys`,
**not** a shared-form key (`SHARED_FORM_CONFIG_KEYS` = `timeout`,
`tunnelCheckUid`, `ipVersion`) and **not** in `secretFields`. Exported so the
unit test exercises the real assembly rather than a re-implementation.

### Step 4 — wire it into `check-form.tsx`

- New state `passthroughSource: { type, config }`, seeded from
  `{ initialType, initialData?.config ?? {} }`, replaced by the sample's config
  in `applySample`, and reset to `{ newType, {} }` on a type switch. The memo
  only uses it when `passthroughSource.type === type`, so a type switch can
  never smuggle an `http`-only key into a `tcp` payload.
- `currentConfig` calls `assembleSubmittedConfig`, feeding both the live
  preview/validation and the submitted payload (unchanged single-source rule).

### Step 5 — model request `body` and `headers` in the HTTP form

- `KeyValueRows` primitive: `src/components/ui/key-value-rows.tsx` (stacked at
  <640px so 375px needs no horizontal scroll), added to the design reference.
- `HttpState` gains `body: string` and `headers: {key,value}[]`.
  `HttpOptionsFields` (Advanced) renders a `Textarea` for the body — visible for
  any method other than `GET`/`HEAD`, hidden-but-preserved otherwise (written
  whenever non-empty) — and a `KeyValueRows` editor for plain headers. Plain
  headers round-trip on GET, so no dirty flag: an empty editor omits the key,
  which clears it.
- `seedExpectedStatusCodes` reads `expected_status_codes` and `expected_status`
  as further fallbacks; `toConfig` keeps writing `expectedStatusCodes` only.
- `httpOptionsSummary` mentions body/headers so the collapsed section shows them.
- Locale keys in all four locales.

### Step 6 — tests

- `types/http.test.ts` (extend): round-trip through `assembleSubmittedConfig`
  preserving `body_expect`/`headers_pattern`/unknown keys; snake
  `expected_status` seeding; owned-key omission still clears
  (`jsonPathAssertions`); secret fields never passed through; a table test over
  **every** registered module asserting `ownedKeys` is declared and covers every
  key `toConfig` writes for a fully-populated state.
- `e2e/check-http-request-body-headers.spec.ts`: API-create → save untouched →
  `GET` asserts nothing was dropped and `period` survived; edit body/headers
  through the UI; clear headers → key gone; `GET`↔`POST` method switch keeps
  the body.
