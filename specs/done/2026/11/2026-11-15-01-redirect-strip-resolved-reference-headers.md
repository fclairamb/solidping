---
model: opus
effort: high
---

# Cross-origin redirects resend headers that carry a resolved `${param:}` / `${env:}` value

## Problem

Found by the security scan of 2026-11-15 (finding AN1). Severity: Low. Nobody has to act: it needs a check whose headers hold a reference to an org variable, and a target that redirects to another origin (a third-party endpoint, a compromised host, or a MITM on plain http).

`${param:…}` and `${env:…}` references are resolved in every string of the effective config, nested maps included (`server/internal/secretref/secretref.go:173-219`, called from `server/internal/checkworker/worker.go:1117-1127`). The docs treat a referenced value as a secret (`web/docs/docs/features/config-as-code.md:49-52`). The redirect strip does not:

- **HTTP check.** On a cross-origin hop, `CheckRedirect` strips only the names listed in `secretHeaders` plus `Authorization` (`server/internal/checkers/checkhttp/checker.go:433-436`, `stripSecretHeaders` at `:1216`). A plain `headers` entry such as `X-Api-Key: ${param:api-key}` is set at `:379` and is sent to the new origin with its resolved value.
- **JS check.** `stripRedirectHeaders` (`server/internal/checkers/checkjs/checker.go:1350`) does nothing when the check has no `secrets` (`len(secrets) == 0`). The checker itself states that a reference in `env` or in the script carries a real value once resolved (`checkjs/checker.go:157-163`). So a script that builds a header from such a value sends it to any origin the target redirects to.

AK6 (`specs/done/2026/11/2026-11-12-06-…`), AL2/AL3 (`2026-11-13-04`, `2026-11-13-05`) and AM2/AM3 (`2026-11-14-02`) closed the same leak for declared secrets, Referer and bodies. This is the remaining header case.

## Proposal

1. Make the resolved reference values known to the checker. When the worker builds the effective config (`checkworker/worker.go:1117-1127`), collect every string a `${param:}` / `${env:}` reference resolved to (extend `secretref.ResolveConfig`, or add a collecting `ResolveFunc` wrapper), and put them on the execution context with a new `checkerdef.WithResolvedReferenceValues(ctx, []string)` / `ResolvedReferenceValuesFrom(ctx)`. Skip empty strings. Never log or output the values. Do this for the in-process path and the agent path (the agent's own merge of the sealed overlay).
2. HTTP check: on a cross-origin hop, also delete every header whose value contains a resolved reference value, and add its name to `redirect_dropped_secret_headers` (`checkhttp/checker.go:433`).
3. JS check: merge the resolved reference values into the set `stripRedirectHeaders` matches against (`checkjs/checker.go:1180`, `:1350`), so a check with references counts as "has secrets". Every header outside `redirectSafeHeaders`, and a safe header whose value contains such a value, is then dropped on a cross-origin hop. Pass the values to the isolated child the same way `Secrets` are passed today (`checkjs/isolation.go`).
4. Update `web/docs/docs/features/check-types.md` (HTTP redirects) and `web/docs/docs/features/javascript-checks.md`: a header holding a variable reference is treated like a secret header on a redirect to another origin.

## Tests

- `checkhttp/checker_test.go`: a check with `headers: {X-Api-Key: <value>}` where the value is registered as a resolved reference value. The origin answers 302 to a second test server. Assert the second server receives no `X-Api-Key` and the output lists it in `redirect_dropped_secret_headers`. This fails before the fix. Positive control: the same header without a registered value still reaches the second server, and a same-origin redirect keeps the referenced header.
- `checkjs/redirect_*_test.go`: a script with no `secrets` that sets `X-Api-Key` from a referenced env value, redirected cross-origin. Assert it is dropped, both in-process and isolated. Positive control: `User-Agent` is kept.
- `secretref` or `checkworker` unit test: resolving `{"headers":{"X":"${param:k}"},"url":"https://a/?t=${param:k2}"}` reports both resolved values, and a config without references reports none.

## To verify

- The agent-side merge path that applies `ParamOverlay` and resolves `${env:}` (look for the second caller of `secretref.ResolveConfig` on the agent), so step 1 covers it.
