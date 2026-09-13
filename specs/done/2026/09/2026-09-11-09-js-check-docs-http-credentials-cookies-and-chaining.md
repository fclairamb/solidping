---
model: sonnet
effort: high
---

# The `js` check docs are four bullet points — nothing says how to call `http`, hold a credential, keep a session across calls, or chain requests

## Problem

The JavaScript check is the type that covers everything the fixed types cannot
express — "log in through the real form, then hit the authenticated endpoint" —
and it is the least documented type we ship. Its entire documentation is
[`check-types.md:1175-1185`](web/docs/docs/features/check-types.md:1175): a
one-line description, the minimum period, and four use-case bullets
("Complex multi-step API workflows", "Aggregating multiple checks into one").
The bullets promise the workflows; nothing says how to write one.

Not one element of the runtime's API surface is documented anywhere in
`web/docs/`:

| What the engine exposes | Where it lives | Documented? |
|---|---|---|
| The return contract — `{ status, metrics?, output? }`, `status` ∈ `up` / `down` / `timeout`, anything else (including `"error"`) is an error, a missing object is an error | [`checker.go:467-512`](server/internal/checkers/checkjs/checker.go:467) | no |
| `http.get/post/put/patch/delete/head(url, { body, headers })` → `{ statusCode, body, headers, duration }` or `{ error }` | [`checker.go:371-465`](server/internal/checkers/checkjs/checker.go:371) | no |
| `env.NAME` — read-only, from the `env` config map | [`checker.go:179`](server/internal/checkers/checkjs/checker.go:179) | no |
| `solidping.check(type, config)` and the typed wrappers `solidping.http/tcp/dns/ssl/icmp/smtp/udp/ssh/pop3/imap/websocket/postgresql/ftp/sftp/domain(config)` → `{ status, duration, metrics, output }`; `js` and `heartbeat` refused; server-disabled types refused | [`checker.go:245-368`](server/internal/checkers/checkjs/checker.go:245) | no |
| `console.log/warn/error/info` → captured into `output.console` | [`checker.go:190`](server/internal/checkers/checkjs/checker.go:190) | no |
| `sleep(ms)` | [`checker.go:227`](server/internal/checkers/checkjs/checker.go:227) | no |
| Limits: 20 sub-checks shared by `http.*` and `solidping.*`, 16 KB console, 1 MB response body, 64 KB script, 30 s max timeout, 50 `env` entries | [`checker.go:61-65`](server/internal/checkers/checkjs/checker.go:61), [`config.go:12-17`](server/internal/checkers/checkjs/config.go:12) | no |
| The script is wrapped in a function, so a top-level `return` works; it is synchronous — no `fetch`, no `Promise`/`async`, no `setTimeout`, no `require` | [`checker.go:126`](server/internal/checkers/checkjs/checker.go:126) | no |

Three consequences, all seen on 2026-09-11 while scripting a Keycloak login:

1. **Credentials.** Nothing says where a password goes, so it goes into `env` —
   which is public, plaintext config, returned on `GET` and present in every
   export (`2026-09-11-05` §3). The docs' silence is what made that look fine.
2. **Sessions.** Nothing says there is no cookie jar and that redirects are
   always followed (`2026-09-11-05` §1–2). A user discovers it by asserting on
   a `401` that means "login worked".
3. **Chaining.** A bearer-token flow — `POST /login`, `JSON.parse(resp.body)`,
   use the token on the next call — works today and is exactly what the "multi-
   step API workflows" bullet promises. Nobody knows, because the only example
   in existence is the one-request httpbin sample in
   [`samples.go:9-15`](server/internal/checkers/checkjs/samples.go:9), and it
   lives in the dashboard's sample picker, not the docs.

Two smaller inaccuracies compound this:

- The dashboard's only in-product guidance,
  [`checks.json:947`](web/dash0/src/locales/en/checks.json:947), says the
  status is `"up"`, `"down"`, or `"error"`. The engine's accepted set is
  `up` / `down` / `timeout`; `"error"` only works because it falls into the
  default branch. The one sentence we do publish is wrong.
- The dashboard `js` form owns only `script`
  ([`misc.tsx:613-614`](web/dash0/src/components/checks/form/types/misc.tsx:613)).
  `env` and `timeout` can be set only through the API, the `sp` CLI, or
  config-as-code — and are preserved, not dropped, by a form save thanks to the
  `ownedKeys` mechanism. None of this is written down, so the `env` global reads
  as unreachable from the UI.

## Proposal

### 1. A dedicated page, linked from the check-types section

Create `web/docs/docs/features/javascript-checks.md` (`sidebar_position: 24`,
title **JavaScript checks**), on the model of
[`ssh-tunnels.md`](web/docs/docs/features/ssh-tunnels.md): the check-types
section keeps a three-line summary plus a link, the way the HTTP row links to
the SSH-tunnels page. The page is the reference; `check-types.md` stops being
the only place the type is mentioned.

### 2. Page contents

Every section below documents **what `checker.go` does at the commit being
documented** — the implementer reads the file, not this spec, for the API.

1. **How a script runs.** goja engine, wrapped in a function (top-level
   `return` is fine), synchronous, one execution per region per period, the
   check's `timeout` (default and max `30s`) interrupts the VM. What is *not*
   available: `fetch`, `Promise`/`async`, `setTimeout`, `require`, DOM. Say
   `sleep(ms)` is the only way to wait, and that it counts against the timeout.
2. **Configuration.** A table of the config keys — `script`, `timeout`, `env`
   (and `secrets`, see §3) — with the honest line that the dashboard form edits
   `script` only, that `env`/`timeout`/`secrets` are set via the API, `sp`, or
   config-as-code, and that a form save preserves them.
3. **Result contract.** A table: `status` (exact accepted values, what any other
   value does), `metrics` (numbers, shown as check metrics), `output` (free
   map, shown in the result), and where `console.*` lines land
   (`output.console`). Fix the dashboard help string
   ([`checks.json:947`](web/dash0/src/locales/en/checks.json:947)) to match, in
   every locale.
4. **API reference**, one subsection per global, each with signature, options,
   return shape, and the error shape:
   - `http.<method>(url, options)` — options `body` (string), `headers`
     (object); response `statusCode`, `body` (string, capped at 1 MB), `headers`
     (canonical keys; a repeated header is an array), `duration` (ms); a
     transport failure returns `{ error }` and **no `statusCode`** — say to test
     `resp.error` first. Plus whatever `2026-09-11-05` adds
     (`followRedirects`, `maxRedirects`, `timeout`, `url`, `redirects[]`).
   - `http.session()` — the cookie-jar client from `2026-09-11-05`, if landed
     (see §3).
   - `solidping.<type>(config)` / `solidping.check(type, config)` — the config
     object is the same key set as the check type's config-as-code document;
     `status` / `duration` / `metrics` / `output` come back; `js` and
     `heartbeat` are refused; a type disabled on the server is refused.
   - `env` and `secrets` — the split, what each is for, where each is stored.
   - `console`, `sleep`.
5. **Limits** — one table, values taken from the constants, not retyped from
   this spec.
6. **Full examples.** Complete, copy-pasteable, each ending in a `return`.
   Every example is a fenced `js` block and is compiled and, where marked, run
   by the test in §4:
   - **JSON API health**: `GET`, check `statusCode`, `JSON.parse(resp.body)`,
     assert a field, return `metrics` from the payload.
   - **Bearer-token login, then an authenticated call** — the chaining example.
     `POST` a JSON body, parse the token, send it in `Authorization` on the
     second call. The password comes from `secrets.PASSWORD`, the URL and user
     from `env`.
   - **Form login with a session** — the cookie-jar example: `http.session()`,
     `GET` the login page, `POST` the form with `followRedirects: false`, assert
     the `302` and its `Location`. Immediately below it, the **manual variant
     that works without a jar**: read `resp.headers["Set-Cookie"]`, send it
     back as `Cookie` on the next call, with the explicit caveat that cookies
     set *inside* a redirect chain are lost this way.
   - **Basic auth via header** — see the open question on base64 below.
   - **Multi-step workflow with cleanup**: create → read back → delete, `down`
     on the first failing step, the delete always attempted, the step name in
     `output`.
   - **Aggregating sub-checks**: three `solidping.*` calls, worst status wins,
     each sub-result's `duration` reported as a metric.
   - **Time-conditional check**: `up` outside business hours without calling
     anything, with the note that the check still runs on its period.
   - **Config-as-code**: the same check as a document — `script: |` block,
     `env:` map, `secrets:` with a `${param:…}` reference (`2026-09-11-03`), so
     nothing sensitive is in git.
7. **Troubleshooting**: `script must return a result object`, `sub-check limit
   of 20 exceeded`, a `timeout` status with an unfinished `sleep`, "my
   `env.X` is `undefined`" (set via API, not the form), and where to read
   `console` output in the result view.

### 3. Sequencing against `2026-09-11-05`

This spec documents the cookie jar, the redirect options and the `secrets` map
that `2026-09-11-05` introduces. It is filed after it and must be implemented
after it. **Documenting an API that does not exist is worse than the current
silence**: before writing the `http.session()` and `secrets` sections, grep
`checker.go` for `session` and `registerSecrets`. If `05` has not landed,
write the page for the runtime as it is — including a plain statement that
there is no cookie jar and that `env` is public — and leave a `TODO(05)`
comment in the markdown where the two sections go, rather than inventing them.

### 4. Examples are tested, not trusted

Add `server/internal/checkers/checkjs/docs_examples_test.go`:

- Parse `web/docs/docs/features/javascript-checks.md`, extract every fenced
  `js` block, and run each through the same `goja.Compile` wrapper `Validate()`
  uses ([`config.go:107-112`](server/internal/checkers/checkjs/config.go:107)).
  A syntax error in the docs fails the build.
- Blocks preceded by an HTML comment `<!-- test: <name> -->` are also
  *executed* against an `httptest` server that implements the fixtures the
  examples need (a JSON health endpoint, a `/login` that issues a bearer
  token, a form login that sets cookies and `302`s, a create/read/delete
  resource). The test rewrites the example's `env.BASE_URL` to the fixture and
  asserts the returned `status`. The samples in `samples.go` go through the
  same harness.
- The negative that matters: a test that changes the fixture's login to reject
  the password and asserts the chaining example returns `down`, not `up` — so
  the example demonstrably checks the credential rather than the transport.

### 5. Keep the dashboard sample picker in sync

Promote two of the doc examples — the bearer-token chain and the sub-check
aggregation — into `GetSampleConfigs` in
[`samples.go`](server/internal/checkers/checkjs/samples.go), with `env` keys
the user has to fill in. The sample picker and the docs then show the same
scripts, and the §4 harness keeps both honest.

### Open questions

- **No base64 in the runtime.** goja has no `btoa`/`atob` (browser APIs, not
  ECMAScript), and `checker.go` registers none. A "Basic auth via header"
  example therefore cannot build the header in-script. Either document the
  workaround (store the precomputed `Basic …` value in `secrets`, or use
  `solidping.http({ basicAuth: … })` for that one call) or add a tiny
  `base64.encode/decode` global — the latter is a runtime change and belongs in
  a follow-up, not in a docs spec. Decide and say which in the page.
- **`timeout` as a returnable status.** The engine accepts it; should scripts
  be told to return it, or is it reserved for the runtime? Document whichever
  the code owners decide, and make `checks.json:947` say the same thing.

### Out of scope

- Any change to the runtime's API (that is `2026-09-11-05` and the base64
  follow-up above).
- Exposing `env` / `secrets` / `timeout` editors in the dashboard `js` form —
  worth doing, separate spec.

## Resolved open questions

Answered by the repository owner on 2026-09-12. These are decisions, not
suggestions — implement them as written.

> **No base64 in the runtime.** goja has no `btoa`/`atob` … Either document the
> workaround (store the precomputed `Basic …` value in `secrets`, or use
> `solidping.http({ basicAuth: … })` for that one call) or add a tiny
> `base64.encode/decode` global — the latter is a runtime change and belongs in
> a follow-up, not in a docs spec. Decide and say which in the page.

**Decision: add the `base64.encode` / `base64.decode` globals in THIS spec.**
The owner chose the runtime change over documenting a workaround, so this spec
is no longer docs-only. Its "Out of scope — any change to the runtime's API"
line is **superseded for base64 specifically** and nothing else.

What that means concretely:

- Register a `base64` global in
  [`checkjs/checker.go`](server/internal/checkers/checkjs/checker.go) alongside
  the existing globals, exposing `encode(string) string` and
  `decode(string) string` over Go's `encoding/base64` **standard** encoding
  (`StdEncoding`, with padding — that is what HTTP Basic requires).
- `decode` on malformed input must **throw a JS error**, not return an empty
  string or a partial decode. A silent empty string would turn a typo'd
  credential into a check that probes with an empty password and reports `down`,
  which is the same class of "nothing visibly happened and the data is wrong"
  failure this batch has been fixing all day.
- Decide and document the byte-safety boundary: goja strings are UTF-16, so
  `decode` of arbitrary binary is not representable as a JS string. Restrict the
  documented contract to text (which is all Basic auth and JSON tokens need) and
  say so on the page rather than implying binary round-trips.
- **Write the Basic-auth doc example with `base64.encode`**, as the owner's
  choice intends — that was the whole point of the question. Still mention that
  `basicAuth` on the `http` helper exists and is simpler for the single-call
  case, so a reader is not pushed into hand-rolling a header they do not need.
- Unit-test it in `checkjs`: a round trip, a known vector
  (`base64.encode("user:pass") === "dXNlcjpwYXNz"`), a padding case, and the
  malformed-input throw — with the throw asserted, since it is the behaviour a
  regression would silently drop.
- This is now a Go change as well as a docs change, so the backend QA targets
  (`make build-backend lint-back test`) apply on top of `make build-docs`.

> **`timeout` as a returnable status.** The engine accepts it; should scripts be
> told to return it, or is it reserved for the runtime?

**Decision: `timeout` is reserved for the runtime.** Document that a script
returns only `up`, `down` or `error`, and that `timeout` is what the runtime
reports when it cuts a script off — a script must not return it. Rationale: a
script returning `timeout` for a slow upstream it merely measured makes the
status mean two different things in a check's history, and the one thing it is
genuinely useful for (distinguishing "we killed it" from "it said no") is lost.

Make `checks.json:947` say the same thing. If the engine currently *accepts* a
returned `timeout`, the docs change is the deliverable here — do **not** also
change the engine to reject it in this spec; note it as a follow-up if you think
it is worth tightening.

## Implementation Plan

Landing on `batch/2026-09-11`, after `2026-09-11-05` (already shipped on this
branch: `http.session()`, redirect options, `env`/`secrets` split — see its
archived spec and `checkjs/checker.go`, `config.go`, `httpsession.go`). Note:
`-05` already added an `env`/`secrets` / `http` helper / `http.session()`
write-up directly inside `check-types.md` (lines ~1187-1274) — that content
moves to the new dedicated page rather than being duplicated.

1. **Base64 runtime globals** (`checkjs/checker.go`): `base64.encode(string)
   string` / `base64.decode(string) string` over Go's `encoding/base64`
   `StdEncoding`. `decode` throws (`vm.NewGoError`) on malformed input rather
   than returning a partial/empty string. Unit tests in a new
   `base64_test.go`: round trip, known vector
   (`base64.encode("user:pass") === "dXNlcjpwYXNz"`), a padding case, and the
   malformed-input throw.
2. **Dedicated page** `web/docs/docs/features/javascript-checks.md`
   (`sidebar_position: 24`, title "JavaScript checks"), covering: how a script
   runs, configuration (`script`/`timeout`/`env`/`secrets`, dashboard-only-owns
   `script`), the result contract (`up`/`down`/`error`, `timeout` reserved for
   the runtime), the full API reference (`http.*`, `http.session()`,
   `solidping.*`, `env`/`secrets`, `console`/`sleep`, `base64`), limits, full
   tested examples, troubleshooting.
3. **Shrink `check-types.md`'s JS section** to the summary + use cases +
   a link box to the new page, moving the `env`/`secrets`, `http` helper and
   `http.session()` subsections there (rewritten to fold in the base64 example
   and the extra examples).
4. **`checks.json` (all 4 locales)**: clarify that `timeout` is never a value
   a script should return — it is what the runtime reports when it interrupts
   the script.
5. **Test harness** `checkjs/docs_examples_test.go`: parse every fenced `js`
   block from `javascript-checks.md`, `goja.Compile` all of them; blocks
   tagged `<!-- test: name -->` are executed against an `httptest` fixture
   server (JSON health, bearer-token login, form login with cookies/redirect,
   create/read/delete) and asserted on `status`; a negative test flips the
   fixture's login to reject the password and asserts the chaining example
   returns `down`. `samples.go`'s existing sample goes through the same
   harness.
6. **`samples.go`**: promote the bearer-token chain and the sub-check
   aggregation examples into `GetSampleConfigs`, with `env` placeholders.
7. QA: `make build-backend lint-back test` (backend + docs example harness),
   `make build-docs` (Docusaurus build). No dash0 changes needed — the `env`/
   `secrets` editors already exist in `misc.tsx` from `-05`.
