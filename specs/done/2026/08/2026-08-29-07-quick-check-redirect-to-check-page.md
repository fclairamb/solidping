---
model: sonnet
effort: medium
---

# Quick-start check creation should land on the new check's page

## Problem

On a brand-new organization with zero checks, the dashboard shows the
simplified "create your first check" hero
(`web/dash0/src/components/dashboard/empty-state-onboarding.tsx`): three
quick-start chips (HTTP / Ping / SSL), one input, one submit button.

After a successful create, `handleSubmit`
(`web/dash0/src/components/dashboard/empty-state-onboarding.tsx:82-101`) just
clears the input and invalidates the `["checks", org]` query — the user stays
on the dashboard, which re-renders into the regular dashboard view once the
list refetches. The moment of highest engagement ("I just created my first
check — is it up?") lands on a generic dashboard instead of the thing they
created. The check's own page is where the first result, status, and charts
will appear.

## Proposal

After `createCheck.mutateAsync` succeeds, navigate immediately to the new
check's detail page instead of staying on the dashboard:

- `useCreateCheck` (`web/dash0/src/api/hooks.ts:797`) already returns the
  created `Check` from `mutateAsync`, so the `uid` is available in
  `handleSubmit`.
- Use TanStack Router's `useNavigate` and go to
  `/orgs/$org/checks/$checkUid` with `params: { org, checkUid: created.uid }`.
  This mirrors what the full check editor does after creation (see
  `checks.new.tsx` for the existing pattern — follow it if it differs).
- Keep the existing query invalidation (the hook's `onSuccess` already covers
  it) so the checks list is fresh when the user navigates back.
- The error path is unchanged: on failure, stay on the hero with the inline
  destructive alert.

Update the E2E coverage in `web/dash0/e2e/empty-state-onboarding.spec.ts`:
the quick-create test(s) currently assert the dashboard re-renders with the
regular view; they should now assert the browser lands on the check detail
route (`/dash0/orgs/<org>/checks/<uid>`) showing the freshly created check's
name. If a test specifically valued the "dashboard switches out of the empty
state" behavior, cover it by navigating back to the dashboard after the
redirect and asserting the hero is gone.

## Notes

- Only the simplified hero changes behavior here. The full check editor
  (`checks.new.tsx`) already has its own post-create navigation; leave it
  as-is unless it also stays put, in which case aligning it is in scope only
  if trivial.
