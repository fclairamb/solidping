---
model: opus
effort: high
---

# HTTP basic-auth credentials are half-plaintext and have no single home

## Problem

`https://solidping.k8xp.com/dash0/orgs/acmetech/checks/rabbitmq-aws-nonprod`
renders its config as:

```
password: solido
secretHeaders: {}
url: https://b-8882....mq.eu-west-3.amazonaws.com/
username: solidping
```

HTTP basic-auth lives in two unrelated top-level config keys —
`username` and `password` — sitting next to an empty `secretHeaders` map:

- [checkhttp/config.go#L62-L68](server/internal/checkers/checkhttp/config.go#L62-L68)
  declares `Username`, `Password` and `SecretHeaders` as three independent fields.
- [checkhttp/config.go#L384](server/internal/checkers/checkhttp/config.go#L384) —
  `SecretFields()` returns `["password", "secretHeaders"]`. **`username` is not
  listed**, so the username half of every credential is stored in the public,
  queryable `checks.config` JSONB column in cleartext, by design.

So a credential has no single home, and only half of it is protected.

The visible `password: solido` is a *second, separate* problem: that dev
deployment has no `SP_ENCRYPTION_MASTER_KEY`, so `applyEncryption` takes its
documented plaintext fallback and puts the secret keys back onto the public
config ([service.go#L3107-L3117](server/internal/handlers/checks/service.go#L3107-L3117)).
That is a deployment fix, out of scope here — see Follow-ups.

### Pre-existing bug found while scoping: editing an HTTP check wipes its secret headers

Independent of the above, and also in scope for this spec because it is silent
credential loss:

- [check-form.tsx#L492-L494](web/dash0/src/components/shared/check-form.tsx#L492-L494)
  forces `config.secretHeaders = {}` on every HTTP submit when the key is absent.
- The GET response only ever returns the *public* config
  ([service.go#L2066-L2090](server/internal/handlers/checks/service.go#L2066-L2090)),
  so `fromConfig` always produces an empty `secretHeaders`
  ([http.tsx#L29-L45](web/dash0/src/components/checks/form/types/http.tsx#L29-L45))
  and the key is *always* absent on edit.
- `mergePatchConfig` treats an explicit `{}` as "clear"
  ([service.go#L3189-L3197](server/internal/handlers/checks/service.go#L3189-L3197)).

Net effect: editing an HTTP check's URL silently destroys its stored secret
headers, while the form keeps rendering "encrypted — enter new values to
replace" ([http.tsx#L164-L172](web/dash0/src/components/checks/form/types/http.tsx#L164-L172)).
Basic-auth `password` survives only because it is a separate top-level key the
form omits when blank.

## Proposal

Store an HTTP check's basic-auth credential as **one reserved, encrypted key**
`basicAuth: "user:pass"`. Both halves get encrypted at rest. The dash0 form keeps
its Username / Password inputs — only the storage and wire shape changes.

### Decisions taken (do not re-litigate)

1. **A reserved `basicAuth` key, not `secretHeaders.Authorization`.** Folding into
   the `secretHeaders` map was the first instinct and was rejected: `Authorization`
   is a key users can also type by hand in the secret-headers editor, and since GET
   redacts `secretHeaders`, the server cannot distinguish an `Authorization` the
   user just typed from one it minted last save and the merge handed back. That
   forces a pile of extra machinery (always-overwrite folding, split dirty flags, a
   new preserve-on-patch concept separate from `SecretFields`, decode-on-load).
   `basicAuth` inherits the existing secret-preserve-on-patch rule for free, cannot
   collide, and needs no new concepts.
2. **Lazy migration, no backfill sweep.** Legacy rows fold on their next write. The
   checker keeps honoring legacy top-level `username`/`password` **forever**.
   `rabbitmq-aws-nonprod` keeps its plaintext `solido` until someone re-saves it.
3. **Keep the two form inputs.** Users still type a username and a password; the
   folding is a storage concern.
4. **Fix the `secretHeaders` wipe bug** (above) as part of this work, via
   dirty-tracking on the form sections.

## Implementation Plan

### 1. Normalization hook (`checkerdef`)

There is no config-normalization seam today: the service stores the raw request
map and only uses `registry.ParseConfig` to reach `SecretFields`. Add an optional
interface to [checkerdef/interface.go](server/internal/checkers/checkerdef/interface.go),
probed exactly like the existing `credentials.SecretFieldsFor`
([secret_fields.go#L16-L24](server/internal/crypto/credentials/secret_fields.go#L16-L24)):

```go
type ConfigNormalizer interface {
    NormalizeConfig(map[string]any) (map[string]any, error)
}
```

Plus a `NormalizeConfigFor(cfg any, m map[string]any) (map[string]any, error)`
probe helper returning `m` untouched when the config doesn't implement it.

### 2. `checkhttp` implements it ([config.go](server/internal/checkers/checkhttp/config.go))

Add a `BasicAuth string \`json:"basicAuth,omitempty"\`` field, parse it in
`FromMap`, emit it in `GetConfig`, and implement `NormalizeConfig`:

- Read `username`/`password`, then **delete both keys unconditionally** — this is
  what makes clearing work and stops a stray password lingering.
- If `username != ""` → set `basicAuth = username + ":" + password`, **overwriting**
  any preserved value. The unconditional overwrite is what keeps a repeated
  `apply` of the same manifest idempotent.
- Gate the fold on `username != ""` *exactly*, mirroring today's
  [checker.go#L231](server/internal/checkers/checkhttp/checker.go#L231): a
  password-only config sends no auth header today and must keep not sending one.
  Its stray password is dropped — cover that with a test.
- Reject a `username` containing `:` (RFC 7617 forbids it; it decodes
  ambiguously). It **must** be a `checkerdef.NewConfigError` so
  [handler.go#L604](server/internal/handlers/checks/handler.go#L604) maps it to
  400 — note `UpdateCheck` never calls `checker.Validate`, so normalize's error is
  the only validation on the PATCH path.

`SecretFields()` becomes `["basicAuth", "password", "secretHeaders"]`. Keep
`password` so legacy rows stay encrypted and survive a PATCH. **Do not add
`username`** — that would strip it from exports
([service.go#L2493-L2519](server/internal/handlers/checks/service.go#L2493-L2519))
and make `credmigrate` encrypt it into the private blob
([credmigrate.go#L112](server/internal/credmigrate/credmigrate.go#L112)).

### 3. Service wiring ([handlers/checks/service.go](server/internal/handlers/checks/service.go))

Call `NormalizeConfigFor` on the **effective** config — after merge, before
encryption:

- create: [service.go#L998](server/internal/handlers/checks/service.go#L998), on `req.Config`
- patch: [service.go#L1305](server/internal/handlers/checks/service.go#L1305), on
  `applyConfigPatch`'s output

Never normalize the raw patch pre-merge — a folded map would replace the existing
one wholesale and wipe unrelated keys.

### 4. Execution ([checker.go#L231](server/internal/checkers/checkhttp/checker.go#L231))

Prefer `BasicAuth` (split on the first `:` with `strings.Cut`), else fall back to
legacy `Username`/`Password`. The legacy branch is permanent — lazy migration
depends on it. `SecretHeaders` are still applied last
([checker.go#L244-L246](server/internal/checkers/checkhttp/checker.go#L244-L246)),
so an explicit `Authorization` secret header still wins. No other execution change.

**Verified as needing no change:** `check_jobs.config` copies the check row
verbatim and re-merges at claim time (derived, not a second source of truth);
`credmigrate` and export/import work as-is; `resolveSecretRefs`
([apply.go#L207-L219](server/internal/handlers/checks/apply.go#L207-L219)) only
walks top-level strings, and `username`/`password` stay top-level strings in
manifests, so `${env:...}` / `${param:...}` keep resolving. No other reader of
http `config.username` exists (other checkers have their own `username` keys;
`SecretFields` is per-config-type).

### 5. Frontend ([http.tsx](web/dash0/src/components/checks/form/types/http.tsx))

Add two **independent** dirty flags to `HttpState`: `authDirty`, `headersDirty`.

- `fromConfig`: seed each flag from whether the incoming config actually carried
  those values. This is load-bearing three ways — legacy rows (username is public,
  so it round-trips and folds on save), prefill links (`?username=probe`,
  [checks.new.tsx#L73](web/dash0/src/routes/orgs/$org/checks.new.tsx#L73), documented
  at [prefill-check-links.md#L40](web/docs/docs/features/prefill-check-links.md#L40)),
  and plaintext-fallback deployments where the values do come back.
- `toConfig`: when a section is not dirty, **omit its keys entirely** so the merge
  preserves them. When `authDirty` and both inputs are empty → send `basicAuth: null`
  to clear. When `headersDirty` and there are no rows → send `secretHeaders: {}` to clear.
- Remove the `secretHeaders = {}` force at
  [check-form.tsx#L492-L494](web/dash0/src/components/shared/check-form.tsx#L492-L494).
- Render the existing "•••• (encrypted — enter new values to replace)" placeholder
  (pattern at [http.tsx#L164-L172](web/dash0/src/components/checks/form/types/http.tsx#L164-L172))
  against the basic-auth block when `configPrivateKeys` includes `basicAuth`.
- `httpAuthSummary` ([http.tsx#L244-L256](web/dash0/src/components/checks/form/types/http.tsx#L244-L256))
  takes only `state`, so a folded row renders "none" despite having credentials —
  pass `configPrivateKeys` as a second argument and treat a stored `basicAuth` as
  customized.

Per [CLAUDE.md](CLAUDE.md), start from the design reference at
`/dash0/orgs/default/design-reference`. This reuses shipped primitives only, so no
additions to the catalog are expected.

### 6. Docs

- [check-types.md#L31](web/docs/docs/features/check-types.md#L31) — the basic-auth
  row; note credentials are stored encrypted at rest. Do **not** hand-edit
  `web/docs/build/` (generated).
- [prefill-check-links.md#L40](web/docs/docs/features/prefill-check-links.md#L40) —
  the `username` prefill param still works; confirm the wording still holds.

### 7. Tests

Backend (`testify/require`, `t.Parallel()`):

- [checkhttp/config_test.go](server/internal/checkers/checkhttp/config_test.go) —
  `basicAuth` round-trip; `SecretFields` contents; normalize: folds, overwrites a
  preserved value, drops a stray password-only config, rejects `:` in the username,
  no-ops when there's no username.
- [checkhttp/checker_test.go](server/internal/checkers/checkhttp/checker_test.go) —
  **nothing currently asserts `SetBasicAuth` fires at all.** Add: `basicAuth` sends
  the header; legacy `username`/`password` still sends it; an `Authorization`
  secret header still wins over both.
- `handlers/checks` — create with username/password → public config carries none of
  `username`/`password`/`basicAuth` and `configPrivateKeys == ["basicAuth"]`; **a
  legacy row folds on an unrelated PATCH and its auth survives** (the key
  regression); clearing works; and `secretHeaders` survive an unrelated PATCH (the
  wipe-bug fix). Update
  [encryption_test.go#L84-L100](server/internal/handlers/checks/encryption_test.go#L84-L100),
  which currently asserts `password` lands in `ConfigPrivateKeys`.
- [apply_test.go#L260-L332](server/internal/handlers/checks/apply_test.go#L260-L332) —
  `${env:}` / `${param:}` into `password` still resolves, and applying the same
  manifest twice is idempotent.

E2E (`web/dash0/e2e/`) — nothing covers http auth today. Add one spec: create an
HTTP check with basic auth, edit only the URL, assert the credential still works
and any secret headers survive.

### 8. QA

1. `make dev-test`, then drive the real form at `/dash0/orgs/test/checks/new` —
   create an HTTP check with basic auth against a local endpoint that requires it;
   confirm it goes green.
2. `GET /api/v1/orgs/test/checks/<slug>` — assert no `username`/`password`/
   `basicAuth` in the returned config, and `configPrivateKeys: ["basicAuth"]`.
3. Edit only the URL, save, confirm the check stays green and secret headers survive.
4. Legacy path: PATCH a check to plaintext `username`/`password` with a build from
   *before* this change, then re-save on the new build and confirm it folds.
5. `make test`, `make test-dash`, `make lint`, `make fmt`. Per
   [CLAUDE.md](CLAUDE.md), re-run failing Postgres tests with `-p 1` before
   treating a testcontainer flake as a regression.

## Implementation status

Plan sections 1–8 implemented as written. Deltas and findings worth knowing:

- **The normalize error surfaces as HTTP 422, not 400.** It *is* a
  `checkerdef.NewConfigError`, and `handleUpdateError` routes it to
  `WriteValidationError` exactly as the plan intends — that helper just emits
  422 (`VALIDATION_ERROR`) for every config error in this app, not 400. The
  contract ("a client error naming the offending field", verified live) holds;
  only the number in the plan's prose was off.
- **Sibling tests had to move with the fold.** `sealing_write_test.go`
  (spec 2026-07-16-02) built HTTP checks with a *password-only* config, which
  the new gate correctly drops; they now carry a real `username` and assert
  `basicAuth`. Same for the two `apply_test.go` secret-ref tests.
- **`SP_ENCRYPTION_MASTER_KEY` does not reach the config** (pre-existing, out
  of scope, but it changes the Follow-up below): koanf's env loader maps `_` →
  `.`, so the var lands on `encryption.master.key` while the struct tag is
  `master_key` — the server logs "credentials encryption disabled" even with
  the var correctly set. Verified locally; only a YAML `encryption.master_key`
  turns encryption on today. So the first Follow-up is *not* just a deployment
  fix: setting the documented env var on k8xp would not have worked either.
  This is the known multi-word-key quirk other keys work around with a manual
  reader (`applyServerEnv`).
- **Encrypted checks do not execute with their secrets** — the in-process
  worker never decrypts `config_private`, so a folded (or, before this change,
  a `password`-bearing) check runs credential-less and goes DOWN on a server
  with a master key. That is exactly todo spec
  `2026-07-16-05-inprocess-worker-never-decrypts-config-private`, pre-dates
  this work, and is untouched here. QA step 1 was therefore verified on a
  plaintext-fallback server (the credential folded to `basicAuth` reaches the
  wire and the check goes green against a real basic-auth endpoint, while an
  uncredentialed control goes down); the encrypted storage contract (QA 2–4)
  was verified separately against a master-keyed server.
- **E2E is mode-aware** for the same reason: the E2E server (and CI's) sets no
  master key, so `e2e/check-http-basic-auth.spec.ts` asserts the credential
  verbatim under the fallback and switches to the
  `configPrivateKeys` + placeholder assertions when encryption is on. Both
  branches were exercised locally.

## Follow-ups (not in scope)

- `SP_ENCRYPTION_MASTER_KEY` is unset on the k8xp dev deploy — that is *why*
  `solido` is readable there. Worth fixing on the deployment itself.
- A backfill sweep (a check-side analogue of
  [reconcile.go](server/internal/credmigrate/reconcile.go), which today only
  handles connections) if lazy migration leaves too many legacy rows behind.
