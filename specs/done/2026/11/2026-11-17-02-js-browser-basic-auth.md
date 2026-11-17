---
model: opus
effort: medium
pr: pingdom-tms
automerge: false
---

# JS browser checks cannot pass HTTP Basic authentication

## Problem
A page behind HTTP Basic auth makes Chrome raise an auth challenge; the JS check's browser session (`server/internal/checkers/checkbrowser/session.go`) has no way to answer it, so the navigation fails. Pingdom transaction checks have a `basic_auth` step (username, password), so imported checks need it too, and staging sites behind Basic auth are a common real case.

Putting `user:pass@` in the URL is not an answer: Chrome strips it for subresources, it lands in logs and `page.url()`, and it bypasses the check's encrypted secrets.

## Proposal
1. **`page.basicAuth(username, password, opts?)`** in `server/internal/checkers/checkjs/browser.go`, to be called before `page.goto()`. Recommended usage, documented: `page.basicAuth(secrets.USER, secrets.PASSWORD)`; the JS config already carries an encrypted `secrets` map exposed as the `secrets` global (`checkjs/config/config.go:56-66`, `checkjs/checker.go:496-511`).
2. **Mechanism: CDP `Fetch.enable{handleAuthRequests:true}` + `Fetch.authRequired` → `Fetch.continueWithAuth{ProvideCredentials}`**, set up in `checkbrowser.Session`. Not `Network.setExtraHTTPHeaders(Authorization)`: that would send the password to every origin the page loads (CDNs, analytics, third-party scripts).
3. **Scope the credentials to one origin.** Answer a challenge only when the challenging request's origin (scheme + host + port) equals the origin of the next `page.goto()` URL (or `opts.origin` if given). Any other origin: `CancelAuth`. Answer at most **twice** per origin per execution, then cancel, so wrong credentials fail fast instead of looping. A `Fetch.requestPaused` that is not an auth event is continued untouched (pattern `*` with `handleAuthRequests` pauses requests too: continue them, or use an auth-only pattern if CDP allows).
4. **Never expose the credentials**: not in the result output, the step log, errors (`basic auth rejected by https://host` without the username), or the screenshot metadata. The existing secret-redaction of JS output must cover them (check `secrets_test.go`).
5. **Proxies**: challenges with `source: Proxy` are always cancelled (out of scope).
6. **Egress guard**: Fetch interception must not bypass or duplicate the egress checks (`session.go:476-511`); the guarded request flow is unchanged.
7. Document the method next to the other `page.*` methods, with the `secrets.*` example and the origin rule.

## Tests
- E2E (real Chrome) with a local test server requiring Basic auth: right credentials → page loads; wrong → fails after ≤2 attempts with an error that names the origin but not the username/password.
- A subresource from a **second origin** that also challenges → cancelled, credentials never sent (assert on the second server's received `Authorization` headers: none).
- Proxy challenge → cancelled.
- Output/log redaction: the password value appears nowhere in the result JSON.
- `checkjs/browser_test.go` (fake session): binding forwards args; calling it after `page.close()` errors like other methods.

## To verify
- Whether `Fetch.enable` coexists with the `network.Enable` listener already used by the egress watcher (`session.go:477-483`) without double-pausing requests.
