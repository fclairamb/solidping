---
model: sonnet
effort: medium
---

# HTTP checks reject the `QUERY` method, so a safe-with-body endpoint can't be probed the way it is meant to be called

## Problem

`QUERY` is the IETF HTTP method for a safe, idempotent request that carries a
request body (draft-ietf-httpbis-safe-method-w-body, the successor to
`SEARCH`). GraphQL gateways, search backends and query APIs are starting to
expose it: the request is shaped like a `POST` (a body, a `Content-Type`), but
it is cacheable and side-effect free like a `GET`. An operator who wants to
monitor such an endpoint must send `QUERY` with a body, because a `GET` on the
same path either 405s or answers something else.

The HTTP checker refuses it today. The allow-list in
[checker.go:125-138](server/internal/checkers/checkhttp/checker.go#L125-L138)
hard-codes `GET / POST / PUT / DELETE / HEAD / OPTIONS / PATCH` and answers
`invalid HTTP method: QUERY` for anything else. Nothing downstream actually
cares about the verb: request construction at
[checker.go:317-343](server/internal/checkers/checkhttp/checker.go#L317-L343)
uppercases whatever it is given, attaches `cfg.Body` as the reader when the
body is non-empty, and `net/http` sends any token as the request line. So the
gate is the only obstacle, and the fix is a small allow-list change plus every
place that mirrors the list.

The dashboard mirrors the list too, twice, in
[http.tsx](web/dash0/src/components/checks/form/types/http.tsx):

- the method `<Select>` at line 282 enumerates the seven verbs, so `QUERY`
  cannot be picked;
- `METHODS_WITHOUT_BODY` at line 85 is `["GET", "HEAD"]`, which is already
  correct for `QUERY` (the body editor must stay visible, exactly as for
  `POST`), so only the select needs the new entry.

The docs table at
[check-types.md:35](web/docs/docs/features/check-types.md#L35) lists
`GET, POST, PUT, DELETE` as the example set and line 39 says "Request body
(for POST/PUT)". Both should mention `QUERY` so a reader who searches the docs
for it finds it.

## Proposal

Treat `QUERY` exactly like `POST`: a body-carrying verb, no other special case.

**Backend** (`server/internal/checkers/checkhttp/`)

1. Add `"QUERY": true` to `validMethods` in `checker.go`. `net/http` has no
   `http.MethodQuery` constant; use a package-level `const methodQuery =
   "QUERY"` next to the existing sample constants so the literal appears once.
2. Nothing else in the request path changes. Explicitly do **not** add
   `QUERY` to any "safe, so drop the body" branch: the whole point of the
   verb is that it is safe *and* has a body, and the checker already sends
   `cfg.Body` regardless of method (see the comment at `http.tsx:71-75`,
   which documents that the backend ignores a body only on `GET`). Verify
   with a test that a `QUERY` probe with `body` set reaches the test server
   with that body and the configured `Content-Type` header intact.
3. Redirect handling: `net/http`'s client rewrites the method to `GET` on
   301/302/303 only when the original was `POST` (or any non-`GET`/`HEAD`
   for 303). `QUERY` falls into the same branch as `POST`, so the observed
   behaviour is already "matches POST". Add a test asserting that a 307 to a
   `QUERY` endpoint re-sends `QUERY` with the body, and that
   `followRedirects: false` stops at the 307, so the symmetry is pinned rather
   than assumed.
4. Tests, table-driven alongside the existing `"invalid http method"` case in
   `checker_test.go`:
   - `Method: "QUERY"` validates;
   - lowercase `query` validates and is sent uppercased (existing
     `strings.ToUpper` path);
   - a `QUERY` request with `body_expect` / `json_path` assertions evaluates
     the response body (the `bodyDrivesAssertions` gate at `checker.go:460`
     is method-agnostic; a positive test prevents a future regression there);
   - the negative control `"INVALID"` still fails, so the allow-list is not
     accidentally opened.
5. `config_test.go`: `QUERY` round-trips through `ToConfigMap` / parse
   unchanged.

**Dashboard** (`web/dash0/src/components/checks/form/types/http.tsx`)

6. Add `"QUERY"` to the method array at line 282, after `"PATCH"` (keep the
   body-carrying verbs together: `GET, POST, PUT, PATCH, QUERY, DELETE, HEAD,
   OPTIONS`). `METHODS_WITHOUT_BODY` is untouched, so the body editor shows
   for `QUERY`. If an E2E in `web/dash0/e2e/` iterates the method select,
   extend it; otherwise add one assertion that selecting `QUERY` keeps the
   body editor visible.

**Docs** (`web/docs/docs/features/check-types.md`)

7. Line 35: `GET`, `POST`, `PUT`, `DELETE`, `QUERY`. Line 39: "Request body
   (for POST/PUT/PATCH/QUERY)". One sentence under the HTTP section noting
   that `QUERY` is sent with the body and treated like `POST` for redirects.
8. Check `server/internal/app/openapi/openapi.yaml` for an enumerated
   `method` field on the HTTP check config; the grep above found no enum, so
   this is expected to be a no-op, but confirm before closing.

**Changelog**: one `Added` line under the HTTP check heading.

## Out of scope

- Other draft verbs (`SEARCH`, `PROPFIND`, WebDAV). This spec adds one verb
  that has a clear body-carrying semantic; a free-form method field is a
  separate decision.
- Caching semantics of `QUERY` responses: the checker never caches, so there
  is nothing to do.
