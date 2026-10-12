---
model: sonnet
effort: medium
---

# An HTTP check cannot assert where its redirects end up, so a redirect to a login or parking page still reads as up

## Problem
HTTP checks follow redirects by default (`followRedirects`, `server/internal/checkers/checkhttp/config/config.go:140-145`), up to 10 hops (`server/internal/checkers/checkhttp/checker.go:30`, cap enforced at `:351-353`). `redirectHostPolicy: same-host` can refuse a hop to another host (`checker.go:357-365`), and every followed hop is recorded in the `redirect_chain` output (`checker.go:54`, `:813-819`).

Nothing asserts the destination. `http://acme.com` must land on `https://www.acme.com/`; if a misconfiguration sends it to `https://acme.com/login` or a registrar parking page, the final answer is still a 200 and the check stays up. Body assertions can catch some of these, but only by guessing page content.

The dashboard exposes `followRedirects` and `redirectHostPolicy` (`web/dash0/src/components/checks/form/types/http.tsx:665-719`), and the docs do not describe redirects for HTTP checks at all (`web/docs/docs/features/check-types.md:22-220`).

## Proposal
1. Config. Add `ExpectedFinalURL string` with JSON key `expectedFinalUrl` to `HTTPConfig`, next to `RedirectHostPolicy` (`config/config.go:153`). Parse it in `FromMap` with `resolveKey(configMap, "expectedFinalUrl", "expected_final_url")`, same shape check as `redirectHostPolicy` (`config.go:398-408`). Emit it in `GetConfig` when non-empty (`config.go:513-590`).
2. Normalisation, new `config/finalurl.go` with `NormalizeFinalURL(raw string) (string, error)`, used on both the configured value and the actual URL:
   - absolute `http` or `https` URL required;
   - scheme and host lowercased;
   - `:80` dropped for http, `:443` for https, any other port kept;
   - empty path becomes `/`;
   - fragment dropped (never sent on the wire);
   - path and query kept as sent: no case folding, no parameter reordering, `/x` and `/x/` differ.
3. Validation in `ValidateSpec`, after the `redirectHostPolicy` switch (`config/validate.go:132-140`), error field `expectedFinalUrl`:
   - the value must normalise;
   - `followRedirects: false` with `expectedFinalUrl` is an error. With redirects off the final URL is always `url`, so the field could only restate it;
   - `redirectHostPolicy: same-host` with an expected host different from the `url` host is an error: no allowed chain can reach it.
4. Checker. Right after the protocol assertion (`checker.go:571-579`), before the regex compile and body blocks: when `ExpectedFinalURL` is set, normalise `resp.Request.URL` (the last request sent) and compare. On a mismatch return `failed(...)` with error `expected final URL "<expected>", got "<actual>"`, and `final_url` (the raw actual URL) in the output; `redirect_chain` is already attached by `failed`. Checking before the body means "landed on the login page" is the reported reason, not a body mismatch it caused. When no redirect happened, the actual URL is `url` itself and is compared the same way. When the 10-hop cap stops the chain, the last URL followed is compared. It runs before `info.verdictReached` (`checker.go:738`), so a health check carrying the field gets it too, like `headers_pattern`.
5. Schema and API. Regenerate `server/internal/checkers/schemas/http.json` (`go generate ./internal/checkers/schemas/...`). Add `expectedFinalUrl` to the HTTP config description in `server/internal/app/openapi/openapi.yaml:10941-10960`.
6. Dashboard, `http.tsx`:
   - `expectedFinalUrl` in `HttpState` (`:70-74`), seeded from both spellings like `redirectHostPolicy` (`:200-208`).
   - `toConfig` writes it only when `followRedirects` is on and the value is not empty (`:281-286`). Turning redirects off hides the field and drops the key, since the server would reject it.
   - Both spellings in `ownedKeys` (`:850-878`).
   - An `Input` inside the `state.followRedirects` block (`:684-719`), `data-testid="check-expected-final-url-input"`, a help line on the normalisation rules, and the server error via `getFieldError(errors, "expectedFinalUrl")` with `data-testid="check-expected-final-url-error"`.
   - A "final URL asserted" part in `httpOptionsSummary` (`:833-839`). Strings via `t()` in en/fr/de/es.
7. Docs, `web/docs/docs/features/check-types.md`: add an `expectedFinalUrl` row to the HTTP options table (`:33-44`) and a short "Redirects" paragraph after "Protocol" (`:55`): `followRedirects` (default true, 10 hops), `redirectHostPolicy`, the `redirect_chain` output, and `expectedFinalUrl` with its normalisation rules and the `final_url` failure output.

## Tests
- `server/internal/checkers/checkhttp/config/finalurl_test.go`, normalisation table: `HTTPS://Acme.COM` gives `https://acme.com/`; `https://acme.com:443/x` gives `https://acme.com/x`; `http://acme.com:80` gives `http://acme.com/`; `http://acme.com:8080/` keeps the port; `#frag` is dropped. Negative cases: `/Login` differs from `/login`, `/x` from `/x/`, `?a=1&b=2` from `?b=2&a=1`; a relative URL and `ftp://acme.com` are rejected.
- `config/config_test.go` (validation): `followRedirects: false` plus `expectedFinalUrl` fails on field `expectedFinalUrl`; `same-host` with another host fails; `same-host` with the same host passes; an invalid URL fails. `FromMap` / `GetConfig` round trip keeps the field and reads the snake_case spelling.
- `server/internal/checkers/checkhttp/checker_test.go` with two `httptest` servers:
  - A answers 302 to B, `expectedFinalUrl` = B: up.
  - Same chain, `expectedFinalUrl` = A: down, error names both URLs, `final_url` is B's URL, `redirect_chain` present.
  - A answers 200 directly, `expectedFinalUrl` = A written with an uppercase scheme: up.
  - A answers 200 directly, `expectedFinalUrl` = B: down (the redirect was removed).
  - A redirects to B and B's body fails a `bodyAssertions` leaf, with `expectedFinalUrl` = C: the error is the final-URL message (evaluation order).
- `web/dash0/src/components/checks/form/types/http.test.ts`: seeding from both spellings; `toConfig` drops the key when `followRedirects` is off and writes it when on.
- `web/dash0/e2e/check-http-verify-ssl-follow-redirects.spec.ts`: the input is visible only with redirects on; a value saves as `expectedFinalUrl`; switching redirects off hides it and the saved config has no such key.
- `make test`, `make lint`, `bun run lint:e2e && bun run typecheck:e2e`.

## Resolved open questions

**Decision (user, 2026-10-12): adopt the recommended answer of every question below. Implement exactly those recommendations.**
- Also support asserting the first hop's `Location` without following? Recommended: no, keep one field. `followRedirects: false`, `expectedStatusCodes: ["3XX"]` and a `Location` header assertion (spec 2026-10-12-11) already cover it.
- Exact match after normalisation only, or also a prefix or regex mode? Recommended: exact only for now. It is predictable and covers the http-to-https and apex-to-www cases. Add a mode later if a user needs to ignore a session token in the query.
