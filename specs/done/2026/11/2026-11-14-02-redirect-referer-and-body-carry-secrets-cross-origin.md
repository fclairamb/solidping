---
model: opus
effort: medium
---

# HTTP and JS checks send secret URL parameters (Referer) and secret request bodies (307/308) to the host a cross-origin redirect points to

## Problem

Found by the security scan of 2026-11-14 (findings AM2 and AM3). Severity: low. Who can trigger
it: nobody has to act. It happens when a check holding secret material reaches a target that
redirects to another origin, for example an open redirect, a CDN or SSO hop, or a compromised or
expired target. The host the redirect points to then gets the secrets.

The AK6 and AL2/AL3 fixes (#851, #865) withhold secret headers and `Authorization` on every hop
that leaves the original origin (`checkerdef.SameRedirectOrigin`). They strip headers only. Two
other parts of the request carry the same secrets past that check.

**AM2: Referer.**
- On every redirect except https → http, net/http sets `Referer` to the full previous URL, query
  string included (`refererForURL` in `net/http/client.go`). It does this before calling
  `CheckRedirect`.
- A check URL can hold a secret org parameter, such as `?api_key=${param:…}`. The worker resolves
  it before the request (D4, `checkworker/checkjobsvc/paramrefs.go`).
- checkhttp never touches `Referer` (`server/internal/checkers/checkhttp/checker.go:420-423`).
- checkjs lists `Referer` as safe (`server/internal/checkers/checkjs/checker.go:1293-1301`). It
  drops it only when the value contains a raw secret (`:1308`), so a percent-encoded secret
  passes.
- So the next host receives the resolved secret in the `Referer` header.

**AM3: request body on 307/308.**
- On a 307 or 308, net/http replays the body to the new location, whatever its host, through the
  `GetBody` that `strings.NewReader` provides:
  - `checkhttp/checker.go:342-346`;
  - `checkjs/checker.go:1142`.
- `${param:…}` references are resolved in `body` too (`paramrefs.go`). JS scripts put
  `secrets.*` in bodies.
- Neither `CheckRedirect` touches the body, so the secret body goes to the other origin.

Both run on the platform workers and on private agents. Browsers also replay a body on a
cross-origin 307, but the checks' own model (AK6) binds secrets to the original origin.

## Proposal

1. In both `CheckRedirect` callbacks (`checkhttp/checker.go:400`, `checkjs` `redirectPolicy` at
   `:1238`), on a hop where `!checkerdef.SameRedirectOrigin(via[0].URL, req.URL)`:
   - delete `Referer` always. A monitor gains nothing from sending it, and it can carry the
     resolved URL. Remove `Referer` from `redirectSafeHeaders` and update its comment.
   - if the hop would replay a body (`req.GetBody != nil` or `req.ContentLength > 0`, that is a
     307 or 308), stop following and return `http.ErrUseLastResponse`. The result then shows the
     redirect response. Report why in the output (for example `redirect_body_withheld: true`),
     next to `redirect_dropped_secret_headers`.
2. Put the hop rule in one shared helper in `checkers/checkerdef`, next to `SameRedirectOrigin`,
   so the two checkers cannot drift, as with AL2.
3. Document it in `web/docs/docs/features/javascript-checks.md` and in the HTTP check page:
   cross-origin redirects never carry `Referer`, and a 307/308 with a body is followed only
   within the origin.

## Tests

- `checkers/checkhttp/checker_test.go`, next to the AL2 tests: a check URL with a query
  parameter that redirects (302) to a second `httptest` server on another host. The second
  server receives no `Referer` (regression: fails before the fix). A same-origin hop still
  carries it (positive control).
- checkhttp: a `POST` with a body that gets a 307, and then a 308, to another host. The other
  host receives no request, and the result reports the redirect response and the withheld flag
  (regression). The same 307 to the same origin is followed with the body (positive control).
- `checkers/checkjs`: the same two cases through `redirectPolicy`, including a percent-encoded
  secret in the URL query. Run them both in-process and through the isolated child path, where
  `http.*` runs in the host.
- A table test of the shared helper: same origin, cross host, https → http, a 303 that drops the
  body (followed), and a 307 with a body (refused).

## Open questions

- Refuse every cross-origin 307/308 with a body, or only for a check that holds secret material?
  The worker does not know which parts of a resolved config came from a secret. Recommended:
  refuse them all. A monitor rarely needs to POST across origins on a 307, and the redirect
  response is still reported.

## Resolved open questions

- Refuse every cross-origin 307/308 that carries a body, whether or not the check holds secret
  material; the redirect response is still reported. (autopilot: recommended answer)
