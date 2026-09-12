---
model: opus
effort: high
---

# The `js` check's `http` helper has no cookie jar, cannot stop at a redirect, and has no encrypted place for a credential — a multi-step login cannot be scripted

## Problem

The `js` check exists so that anything the fixed check types cannot express —
"log in through the real form and confirm we get a session" — can be scripted.
Trying exactly that against a Keycloak realm (2026-09-11, the SSO post-mortem
detection work) showed the `http` helper cannot do it, for three independent
reasons in [`checkjs/checker.go`](server/internal/checkers/checkjs/checker.go):

1. **No cookie jar.** `httpRequest` builds a bare `&http.Client{}`
   ([`checker.go:426`](server/internal/checkers/checkjs/checker.go:426)).
   Cookies set by one call are not carried into the next, and — worse —
   cookies set *during a redirect chain* are lost inside a single call, because
   Go only carries them with a `Jar`. Every session-based flow (Keycloak,
   any OIDC provider, any classic web login) needs the `AUTH_SESSION_ID` /
   `KC_RESTART`-style cookies from step 1 on step 2.

2. **Redirects cannot be disabled.** The option map accepts only `body` and
   `headers` ([`checker.go:402-421`](server/internal/checkers/checkjs/checker.go:402));
   the default client follows up to 10 redirects. In an OAuth flow the
   *success* signal is the `302` with `code=` in `Location` — the helper
   follows it into the relying party, which without the RP's cookies answers
   `401`. Measured on the real realm: success = `302 → 302 → 401`, failure =
   `200` with an error message. A check written today has to assert
   `statusCode === 401` to mean "login worked". The `http` check type has
   `followRedirects: false` ([`checkhttp/config.go:113`](server/internal/checkers/checkhttp/config.go:113));
   the scriptable one, which needs it more, does not.

3. **There is no encrypted place for a credential.** The script is public and
   `JSConfig.Env` ([`checkjs/config.go:25`](server/internal/checkers/checkjs/config.go:25))
   — the only channel for "values the script needs" — is a plain public config
   key: `checkjs` declares no `SecretFields()`, so whatever a script needs at
   runtime lives in the public `config` column, comes back on `GET`, and goes
   into every export. A password has nowhere to go but `env`, and `env` is not
   secret. This is not a naming slip to fix by making `env` secret wholesale:
   `env` is the right name for plaintext parameters (a base URL, a username),
   and `SecretFields()` is per-top-level-key, so encrypting `env` would force
   *every* value in it into the private column — you could no longer keep a
   diffable public parameter next to a secret. What is missing is a *second*,
   encrypted map — the `env` / `secrets` split every CI system and Kubernetes
   already makes.

Net: the check that is supposed to cover the long tail is the one check that
cannot hold a credential or drive a two-request flow.

## Proposal

### 1. A session object with a cookie jar

```js
const s = http.session();                 // fresh cookie jar
const page = s.get(authorizeUrl);         // Set-Cookie captured, incl. across redirects
const r = s.post(page.loginAction, { body, headers, followRedirects: false });
return { status: r.statusCode === 302 && /code=/.test(r.headers.Location) ? "up" : "down" };
```

- `http.session()` returns an object with the same `get/post/put/patch/delete/head`
  as `http`, backed by one `http.Client` with a `cookiejar.Jar`. The bare `http.*`
  functions keep today's semantics (stateless) so existing scripts are unaffected.
- Jar contents are readable (`s.cookies(url)` → `[{name, value, domain, path}]`)
  for assertions, never persisted, and bounded (say 100 cookies; 4 KiB each) so a
  hostile target cannot balloon the job.

### 2. Request options that match the `http` check type

- `followRedirects: boolean` (default `true`, as today), `maxRedirects`
  (≤ 10), `timeout` (≤ the check's timeout).
- Response gains `url` (final URL after redirects) and `redirects: [{statusCode,
  location}]` so a script can assert on the chain without disabling following.
- `headers` is returned with canonical keys and multi-value headers as arrays
  (already the case) — document it.

### 3. A second, encrypted `secrets` map — not a secret `env`

Add a `secrets` map alongside `env`, and expose it in the engine under that
name, so the call site reads what it is: `secrets.PASSWORD`, not `env.PASSWORD`.

| map | stored | engine global | for |
|---|---|---|---|
| `env` (existing) | public `config`, plaintext, diffable | `env.BASE_URL` | non-secret parameters |
| `secrets` (new) | encrypted `config_private` | `secrets.PASSWORD` | credentials |

- `JSConfig` gains a `Secrets map[string]string` field (json `secrets`), and
  `checkjs` declares `SecretFields() = ["secrets"]` — modelled exactly on
  `checkhttp`'s `secretHeaders`, which is already a `map[string]string` declared
  secret ([`checkhttp/config.go:519`](server/internal/checkers/checkhttp/config.go:519)),
  so `credentials.SplitConfig`
  ([`credentials/split.go:33`](server/internal/crypto/credentials/split.go:33))
  moves the whole map into the encrypted envelope with no new machinery. Values
  are never returned on `GET`, redacted from export, and preserved on
  import/apply when the key is absent (the standard secret-merge via
  [`mergePatchConfig`](server/internal/handlers/checks/service.go:4797)).
- A `registerSecrets()` mirrors `registerEnv()`
  ([`checker.go:179`](server/internal/checkers/checkjs/checker.go:179)) — a
  read-only `secrets` object built from the decrypted-effective config, so the
  runtime reads `secrets.NAME` exactly as it reads `env.NAME` today. The
  effective config the runtime sees (public ∪ decrypted-private) already carries
  both maps by the time `Execute` calls `FromMap`, so nothing else in the engine
  changes.
- **Why add, not rename.** `env` is a shipped config key *and* a shipped engine
  global; renaming it to `secrets` turns every existing script's `env.X` into
  `undefined` and silently drops the `env:` key from config-as-code documents
  and the export format — a breaking change to self-hosted deployments we cannot
  survey. Additive is non-breaking and, per the table above, strictly better: it
  keeps a plaintext home for non-secret parameters instead of encrypting them by
  side effect. (If a later audit shows `env` has no real users, a clean rename
  with a compatibility alias is a separate, smaller decision — not this spec's.)
- The dashboard's `js` form gains a `secrets` key/value editor treated like
  `secretHeaders`: dirty-flagged, values write-only after save (design-reference
  pattern). `env` keeps its existing plaintext editor. Omitting the two failure
  modes here re-introduces the "save wipes the secret" class of bug
  (`2026-05-18-07`, `2026-08-28-12`).
- Config-as-code: `secrets` values accept `${env:}`/`${param:}` references
  (`2026-09-11-03`), so a tracked script carries `secrets: { PASSWORD:
  "${param:sso-authtest-password}" }` and nothing sensitive in git.
- Migration is a non-issue by construction: nothing moves. Existing `env` stays
  public and untouched; a check that needs a credential moves that one value
  from `env` to `secrets` on its next edit. No lazy fold, no `credmigrate` pass.
  (The live `sso-keycloak-login-js` check created on 2026-09-11 keeps its
  password in `env` until then — it must be moved to `secrets` once this ships.)

### 4. Tests that prove the negatives

- `httptest` server: `GET /a` sets two cookies and 302s to `/b`, which sets a
  third; `POST /c` echoes the `Cookie` header. Assert a `session` carries all
  three and bare `http.post` carries none.
- `followRedirects: false` returns the `302` with `Location`; default follows
  and reports `redirects.length === 1`.
- `GET /checks/:uid` on a `js` check with a `secrets` map returns no `secrets`
  values but **does** return `env` values (the two-map split, proven in both
  directions); `/checks/export` redacts `secrets`, keeps `env`; an import
  omitting `secrets` preserves it while a change to `env` still applies (run the
  check and assert the script sees both `secrets.X` and `env.Y`).
- Leak guard: after a create/update with a `secrets` value, assert the fixture
  secret does not appear anywhere in the public `config` column (the check
  `credentials.SplitConfig` and the registry audit already back for other
  types).
- Dash0 e2e: create a `js` check with both an `env` and a `secrets` value
  through the UI, reload the edit page — `env` shown, `secrets` not — save
  without touching either, and assert via a result that the script still reads
  both.

### Out of scope

- A `fetch()`-compatible API or Promise support in the goja runtime.
- Browser-check scripting (form filling) — the `browser` type is URL + selector
  + keyword only; a real "scripted browser" is a different spec.

---

## Implementation Plan

Landed on `batch/2026-09-11`, on top of `-01` (`ownedKeys` passthrough), `-02`
(`ExportRedactedFields`), `-03` (`${env:}`/`${param:}` stored verbatim) and
`-04` (document-validate + dry-run `unchanged`).

### 1. `secrets`, the encrypted second map (`checkjs/config.go`)

- `JSConfig` gains `Secrets map[string]string` (json `secrets`). `env` is
  untouched: same key, same plaintext column, same engine global.
- `FromMap`/`GetConfig` grow a shared `stringMapFromConfig` / map emitter so the
  two maps cannot drift; `Validate` caps `secrets` at the same 50 entries `env`
  has.
- `SecretFields() []string { return []string{"secrets"} }` in a new
  `checkjs/secret_fields.go`, modelled on `checkhttp`'s `secretHeaders`. That is
  the *whole* server-side wiring: `credentials.SplitConfig`, the read-time
  redaction, `mergePatchConfig`'s preserve-absent rule, the exporter's
  `SecretFields ∪ ExportRedactedFields` strip, the check-types metadata's
  `secretFields`, and `-04`'s masked diff are all generic over that one
  declaration. No `ExportRedactedFields` entry: a declared secret field is
  already stripped from export by `-02`.
- `${env:}`/`${param:}` inside `secrets` needs no new code either —
  `secretref.ResolveConfig` already walks nested maps, and `ParamOverlay`
  returns the whole `secrets` key as one overlay entry. A test pins it rather
  than a second resolution path.

### 2. `registerSecrets()` (`checkjs/checker.go`)

Mirrors `registerEnv()`: a read-only `secrets` object built from the effective
(public ∪ decrypted-private) config the runtime already receives.

### 3. Cookie jar + redirect control (`checkjs/checker.go`, new `httpsession.go`)

- `http.session()` returns an object carrying the same six verbs plus
  `cookies(url)`, backed by a **bounded jar** (100 cookies, 4 KiB each) wrapping
  `net/http/cookiejar`. Bare `http.*` keeps `jar == nil` — today's stateless
  semantics, byte for byte.
- The jar is shared across per-request `http.Client`s rather than one client
  being shared: that is what lets `followRedirects` / `maxRedirects` / `timeout`
  be per-request while cookies stay per-session.
- `cookiejar.Jar.Cookies()` only returns name+value, so the wrapper records
  domain/path as it observes `SetCookies` and decorates the snapshot — which is
  also where the bound is enforced.
- Options: `followRedirects` (default `true`), `maxRedirects` (≤ 10), `timeout`
  (string duration or ms number, clamped to the check's timeout).
- Response gains `url` (`resp.Request.URL`) and `redirects[]` recorded in
  `CheckRedirect` from `req.Response`. With `followRedirects:false` the 302
  itself is returned and `redirects` is empty — the chain was not walked.

### 4. Dash0 `js` form (`form/types/misc.tsx`)

- `jsModule` grows an `env` plaintext editor and a `secrets` write-only editor,
  both on the shared `KeyValueRows` primitive (`secretValues` for the latter).
  `ownedKeys: ["script", "env", "secrets"]`. The `-01` table test enforces it —
  but only after this spec extended it. `undeclaredKeysFor` drove each module
  from `fromConfig` alone, and a write guarded by a DIRTY FLAG is unreachable
  that way: `secrets` is written under `if (state.secretsDirty)` and
  `fromConfig` hard-codes that false (a secret map never comes back on a read,
  so there is nothing to seed it from). The audit therefore reported `jsModule`
  clean with `secrets` REMOVED from `ownedKeys` — it was auditing a write that
  could not fire. A second pass per seed, over a state with every boolean
  flipped true, makes it reachable, and a committed positive control pins that:
  it fails if the pass is removed.
- `secretsDirty` reproduces `HttpAuthFields`' contract through the shared
  primitive: a row count that GREW is an "add" and does not dirty; an edit or a
  removal does. Not dirty ⇒ `secrets` is absent from the payload ⇒ the server
  preserves it. `configPrivateKeys.includes("secrets")` renders the `••••`
  stored-value line.
- Locale keys in all four locales (`locale-parity.test.ts` guards it).

### 5. Tests

- `checkjs`: `httptest` cookie/redirect suite (session carries three cookies
  across a redirect, bare `http.post` carries none; `followRedirects:false`
  returns the 302 + `Location`, default reports `redirects.length === 1`;
  `url` is the final URL); jar bound; `secrets.X` visible and distinct from
  `env.Y`; config round-trip.
- `handlers/checks`: GET returns `env` values and no `secrets` (both
  directions); export redacts `secrets`, keeps `env`; an import omitting
  `secrets` preserves it while an `env` change applies, then the stored
  effective config is run through the checker and the script reads both;
  `${param:}` in `secrets` stays a reference at rest and resolves at execution.
  Leak guard: `-03`'s `registerLeakGuard` with one new needle.
- dash0 unit: `misc.test.ts` for the dirty rule and the omit-to-preserve
  payload. E2E `check-js-env-secrets.spec.ts`: create with both through the UI,
  reload (env shown, secrets not), save untouched, then poll for a real result
  proving the script still read both.
- Docs: the `js` section of `web/docs/docs/features/check-types.md`.
