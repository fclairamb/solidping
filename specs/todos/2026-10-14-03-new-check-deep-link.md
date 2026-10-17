---
model: sonnet
effort: medium
---

# An org-less "monitor this" deep link that survives signup and lands on a prefilled new-check form

## Problem
www.solidping.io is getting free tool pages (`solidping-website` spec `2026-10-14-01-free-tools-pages`): run one check, then "monitor it every minute, free". That CTA needs a link that a signed-out visitor can follow into signup and come out on a filled-in new-check form.

The pieces exist but don't connect:
- `checks.new` already reads a prefill query (`web/dash0/src/routes/orgs/$org/checks.new.tsx:71`: `checkType`, `checkPeriod`, `checkName`, `checkSlug`, `httpUrl`, `httpMethod`…). It lives under `/orgs/$org/`, and the website can't know the visitor's org slug, or whether they have an org at all.
- `/login` keeps `returnTo` (`web/dash0/src/routes/login.tsx`), and `/` resolves the user's org (`routes/index.tsx`, `RootRedirect`, `solidping_org` in localStorage). Neither has a "then go to the new-check form of whatever org you end up in" path.
- A brand-new user goes through registration, email confirmation (`confirm-registration.$token.tsx`) and org creation. The query has to survive all three, and the confirmation step usually happens in another tab, from the email.

## Proposal
1. New route `/monitor` (served at `/d/monitor`). It accepts the same keys as `checks.new` and forwards only those keys (an allowlist, never arbitrary params).
   - Signed in with an org: navigate to `/orgs/$org/checks/new` with the query.
   - Signed in, no org: go through the existing no-org flow, then to the form.
   - Signed out: go to `/login` with `returnTo=/monitor?…`, so login and the existing returnTo handling bring the visitor back here.
2. Survive the email confirmation: before leaving for registration, keep the pending prefill in `localStorage` (`solidping_pending_check`, with a 24 h expiry). On the first authenticated load of `/` or after org creation, consume it once, then delete it.
3. The demo org must not receive it: with `demo=true`, ignore the pending check.
4. The prefill only fills the form. Nothing is created until the user presses Save, as today.

## Tests
- Unit: allowlist filtering (an unknown key is dropped), the pending-check expiry, single consumption.
- Playwright: signed-out `/d/monitor?checkType=http&httpUrl=https://example.com` → login → org → the form shows `https://example.com`. Same for a signed-in user, who lands on the form directly. `data-testid` on anything new.

## To verify
Manual on staging: register a fresh account from a `/d/monitor` link, confirm from the email in another tab, create the org, and check the form is prefilled.

## Open questions
1. Should the pending prefill also go into the registration request, server side, instead of localStorage? Recommended: localStorage. It is not sensitive, and it keeps the server out of it. The cost is that a visitor who confirms on another device loses the prefill, which is acceptable.
