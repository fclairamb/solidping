# Signup attribution: OAuth sign-ups and offline conversion upload

**Status:** backlog — follow-up to the email-registration half, which shipped
as `users.signup_attribution` (migration `020_v0_26_0`).

## Context

www.solidping.io forwards the ad click identifier (`gclid`, `gbraid`,
`wbraid`, `msclkid`) and the `utm_*` tags on every link into the dashboard.
The dashboard now reads them on load (`web/dash0/src/lib/attribution.ts`),
sends them with `POST /api/v1/auth/register`, and the server stores them on
the user at email confirmation. The point is to be able to tell the ad network
"this click became an account" without loading any advertising script in the
dashboard.

Two gaps remain.

## 1. OAuth / OIDC sign-ups lose the attribution

Signing in with Google, GitHub, GitLab, Microsoft, Slack or Discord leaves
the app for the provider and comes back with a full page load, so the
in-memory attribution is gone before `createUserAndCapture` runs.

Proposed: the login page passes the attribution into the OAuth start request
(the same way `returnTo` already travels), the server keeps it in the OAuth
state entry, and every find-or-create path sets `user.SignupAttribution`
before calling `createUserAndCapture`. Keep the closed key set and the 200
character cap; reuse `normalizeSignupAttribution`.

## 2. Offline conversion upload

With the click id and `users.created_at` in hand, a scheduled job (or the
billing service, which already knows plan changes) can upload conversions to
Google Ads through the Offline Conversions API: `gclid`, conversion time,
conversion action name, optional value. No cookie, no script, no consent
banner in the dashboard.

Proposed: a `solidping-billing` job that selects users created in the last
N days with `signup_attribution->>'clickIdKind' = 'gclid'` and not yet
uploaded, uploads them, and records the upload timestamp. Needs a Google Ads
API developer token and the conversion action created for the "Started
signup" click conversion the marketing site already fires (see
`docs/marketing/google-ads.md` in the website repository, §4 and §7).

## Out of scope

- Any attribution UI beyond a read-only field on the super-admin user page.
- Storing attribution in browser storage. The memory-only choice is
  deliberate and documented in `attribution.ts`; do not "fix" the reload
  case by adding sessionStorage without updating the public cookie policy.
