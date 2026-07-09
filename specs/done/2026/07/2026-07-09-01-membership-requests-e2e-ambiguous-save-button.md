# `membership-requests.spec.ts`'s "unsafe auto-join regex" test fails on strict-mode ambiguity

## Problem

Running `web/dash0/e2e/membership-requests.spec.ts`'s "settings page rejects
an unsafe auto-join regex inline" test against a live server now fails:

```
Error: locator.click: Error: strict mode violation: getByRole('button', { name: /save/i }) resolved to 2 elements:
    1) <button type="submit" ...>Save</button> aka locator('form').filter({ hasText: 'Email pattern (regex)Leave' }).getByRole('button')
    2) <button type="submit" data-testid="session-duration-save" ...>Save</button> aka getByTestId('session-duration-save')
```

`web/dash0/src/routes/orgs/$org/organization.settings.tsx` now has two
separate "Save" buttons on the same settings page — the pre-existing
auto-join regex form's save button
([`organization.settings.tsx:200`](web/dash0/src/routes/orgs/$org/organization.settings.tsx:200))
and a new one added by the session-length-override feature
(`data-testid="session-duration-save"`,
[`organization.settings.tsx:266`](web/dash0/src/routes/orgs/$org/organization.settings.tsx:266)),
introduced in commit `ea08873b feat(dash0): add org settings UI for the
session-length override` (part of this same `batch/2026-07-08` batch).

The test at
[`membership-requests.spec.ts:34`](web/dash0/e2e/membership-requests.spec.ts:34)
does `page.getByRole("button", { name: /save/i }).click()`, which used to be
unambiguous but now matches both buttons, so Playwright's strict-mode check
fails the click.

This is pre-existing test debt from a sibling spec in the same batch —
unrelated to whichever spec was in progress when it was found.

## Proposal

1. Add a `data-testid` to the auto-join regex form's save button at
   [`organization.settings.tsx:200`](web/dash0/src/routes/orgs/$org/organization.settings.tsx:200),
   mirroring the pattern already used for `session-duration-save` (e.g.
   `data-testid="auto-join-pattern-save"`).
2. Update
   [`membership-requests.spec.ts:34`](web/dash0/e2e/membership-requests.spec.ts:34)
   to target that testid instead of the ambiguous role/name locator.
3. Scan the rest of `membership-requests.spec.ts` and any other spec touching
   `organization.settings.tsx` for the same `getByRole("button", { name: /save/i })`
   pattern in case other tests are similarly affected.
4. Verify by running the spec against a live test-mode server:
   ```bash
   cd web/dash0 && CI=true E2E_BASE_URL=<test-mode server>/dash0/ \
     bunx playwright test membership-requests.spec.ts --project=chromium
   ```
   Confirm all tests in the file pass, not just the previously-failing one.
5. Run `cd web/dash0 && bun run lint` before committing.

## Open questions

- None identified yet — scope should be confirmed complete once the grep in
  step 3 comes back clean.
