---
model: sonnet
effort: medium
---

# The rate-limit banner E2E still asserts the pre-2026-08-26-04 link target, and fails

## Problem

`web/dash0/e2e/entitlements-usage.spec.ts:120` ("an org over its per-minute cap
is told so on the checks list and the usage page") fails reproducibly.

At [entitlements-usage.spec.ts:163](web/dash0/e2e/entitlements-usage.spec.ts:163)
the test loads `orgs/test/organization/usage` and asserts:

```ts
// No self-link on the page the reader is already on.
await expect(page.getByTestId("check-rate-limit-usage-link")).toHaveCount(0);
```

The element is present (count 1), so the assertion fails.

**The product code is right; the test's expectation is stale.** Spec
2026-08-26-04 retargeted that link: it used to point at the Usage page, and now
points at the check scheduling page —
[check-rate-limit-banner.tsx:85](web/dash0/src/components/shared/check-rate-limit-banner.tsx:85)
renders `<Link to="/orgs/$org/checks/scheduling">`. The reasoning is recorded at
the call site in
[organization.usage.tsx:125-137](web/dash0/src/routes/orgs/$org/organization.usage.tsx:125):
the banner should hand the reader the surface that can *fix* the overage, not
one that restates the number it just quoted. So on the Usage page the link is
deliberately on, and it is no longer a self-link.

The prop's own contract already says as much —
`showUsageLink` is documented as "Off on the scheduling page itself" — and
`checks.scheduling.tsx:463` is the one call site that omits it, exactly as
documented.

The unit tests were updated with that retarget and are green:
[check-rate-limit-banner.test.tsx:98](web/dash0/src/components/shared/check-rate-limit-banner.test.tsx:98)
("points the remedy link at the scheduling page, not at Usage") and
`:114` ("offers the remedy link only where it was asked for") both encode the
new behaviour. Only the E2E was left behind.

Verified PRE-EXISTING on base commit `1e6dcd927` (`batch/2026-08-26-2` HEAD),
reproduced from a clean `git worktree` build at that commit, on a freshly reset
test DB, with the test run in isolation. It is unrelated to the
entitlements-audits fix made in the same session.

### Secondary: two names now lie

The retarget left the identifiers naming the old destination:

- the test id `check-rate-limit-usage-link` — a link to `/checks/scheduling`
- the i18n key `org:checkRateLimit.viewUsage`

The user-visible *copy* was already updated in all four locales ("Review
scheduling", "Revoir la planification", "Planung überprüfen", "Revisar la
programación") — it is only the key and the test id that still say "usage".
That is how a reader of the E2E concluded the link was a self-link.

## Status — partially shipped in v0.19.0

Steps 1, 2 and 4 are **done and released**; only step 3 remains.

- **Step 1 (done)** — the stale assertion is fixed. `entitlements-usage.spec.ts`
  now asserts the remedy link is present on Usage *and* points at the scheduling
  page, asserting the destination rather than mere presence.
- **Step 2 (done)** — the "no self-link" assertion moved to the scheduling page,
  which previously had no coverage of that invariant.
- **Step 4 (done)** — CI was **not** masking this behind a skipped E2E job. CI had
  never run on `batch/2026-08-26-2` at all: the batch accumulated 144 commits with
  zero pipeline runs until its PR was opened. Guard added since (#271) so a
  non-conventional PR title cannot silently break release parsing the same way.

Landed in commit `107298469`, merged via #269, released in **v0.19.0**.

- **Step 3 (OUTSTANDING)** — the rename below. It was deliberately deferred: it
  touches `check-rate-limit-banner.tsx` and its unit test, which had uncommitted
  in-flight work at the time, and this spec itself says to land steps 1-2 first
  in that case. That work has since been committed (`e401b9e68`), so the rename
  is now unblocked and is all that is left here.

## Proposal

1. **Fix the stale assertion.** On the Usage page, assert the opposite of what
   is there today: the remedy link is present, and it points at the scheduling
   page rather than at the current page. Asserting the destination — not merely
   the presence — is what keeps the test honest if the target moves again.

2. **Move the "no self-link" assertion to where it is actually true.** The
   scheduling page is the surface that must not link to itself. Either extend
   this test with a third stop at `orgs/test/checks/scheduling`, or add a small
   sibling test; the E2E currently has no coverage that the scheduling page
   suppresses the link, which is the invariant the deleted assertion was
   reaching for.

3. **Rename the two stale identifiers** so the next reader is not misled the
   same way: `check-rate-limit-usage-link` → `check-rate-limit-scheduling-link`
   and `org:checkRateLimit.viewUsage` → `org:checkRateLimit.reviewScheduling`.
   Mechanical but wide — the test id appears in the component, the unit test and
   the E2E; the i18n key appears in the component and in all four locale files
   (`en`, `fr`, `de`, `es`). Keep the translated strings
   themselves unchanged; only the key moves. If the rename would collide with
   other in-flight work on these files, land steps 1–2 first and do the rename
   as its own commit.

4. **Check whether CI was masking this.** A green pipeline with a red E2E is its
   own defect — confirm whether the E2E job was skipped behind a red upstream
   job while this landed, and note the finding in the PR either way.

### Verification

The failure is not reproducible against a devloop on `:4000`; use a side-car
test server so the running dev environment is left alone:

```bash
make build-dash0 copy-dash0 && cd server && go build -o /tmp/sp-e2e .
```

```bash
SP_RUNMODE=test SP_SERVER_LISTEN=":4010" SP_DB_TYPE=postgres SP_DB_URL='postgres://solidping:solidping@localhost:55432/solidping?sslmode=disable' SP_DB_RESET=true /tmp/sp-e2e &
```

```bash
cd web/dash0 && CI=true E2E_BASE_URL=http://localhost:4010/dash0/ npx playwright test e2e/entitlements-usage.spec.ts --workers=1 --reporter=line
```

Run the whole `entitlements-usage.spec.ts` file, not just the one test — it
PATCHes the org's real entitlements and shares state across cases. Also run
`bun run test:unit` after the rename in step 3; the banner's unit tests assert
the test id directly.

**The working tree is on a batch branch — do not change the current branch.** If
an isolated branch is genuinely needed, use a separate `git worktree`.
