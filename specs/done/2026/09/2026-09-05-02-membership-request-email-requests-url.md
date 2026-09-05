---
model: sonnet
effort: medium
---

# The "New membership request" admin email links to a dashboard URL that does not exist

## Problem

When someone asks to join an organization, every admin receives the
`membership_request_new.html` email with a **Review pending requests** button.
The button (and the plain-text fallback link) points to:

```
https://solidping.io/dash0/orgs/default/members?tab=requests
```

That path is wrong. dash0 has no `tab` search param on the members page, and
pending requests live on their own route. The correct target is:

```
https://solidping.io/dash0/orgs/default/organization/requests
```

The URL is built in
[server/internal/handlers/auth/membership_requests.go:386](server/internal/handlers/auth/membership_requests.go:386):

```go
requestsURL := fmt.Sprintf("%s/dash0/orgs/%s/members?tab=requests",
    s.fullCfg.Server.BaseURL, org.Slug)
```

The dashboard route that actually renders pending requests is
[web/dash0/src/routes/orgs/$org/organization.requests.tsx:47](web/dash0/src/routes/orgs/$org/organization.requests.tsx:47)
(`/orgs/$org/organization/requests`), registered in the org sidebar at
[web/dash0/src/routes/orgs/$org/organization.tsx:28](web/dash0/src/routes/orgs/$org/organization.tsx:28).
The members page at `organization.members.tsx` never reads a `tab` param, so an
admin clicking the email lands on the members list and has to find the
requests page by hand.

Nothing pins the email URL to the real route today:

- The email-preview fixture uses a **third** path that also does not exist:
  `.../dash0/orgs/acme/organization/membership-requests`
  ([server/internal/handlers/emailpreview/fixtures.go:280](server/internal/handlers/emailpreview/fixtures.go:280)).
- The formatter tests only pass opaque placeholder URLs
  (`https://x.test/requests`), so they cannot catch a wrong path.
- There is no test on the auth service asserting the URL handed to the
  template.

A second defect is visible in the same email (see the screenshot that
motivated this spec): the org name renders empty — *"has asked to join **on**
SolidPing"* and *"you're an admin of **.**"*. `notifyAdminsOfMembershipRequest`
stamps `org.Name` through `email.ApplyOrgBranding`
([server/internal/email/branding.go:35](server/internal/email/branding.go:35)),
so an organization whose `name` column is empty (the long-lived production
`default` org is the observed case) produces a sentence with a hole in it. The
subject line suffers the same way: *"New membership request from X on "*.

## Proposal

1. **Fix the path.** In `notifyAdminsOfMembershipRequest`, build the URL as
   `%s/dash0/orgs/%s/organization/requests`. Keep it a single constant or
   helper next to the other dash0 paths in the auth package (`noOrgPath`,
   `deviceVerificationPath`) so it is easy to spot and grep.

2. **Align the preview fixture** in `emailpreview/fixtures.go` with the real
   route so `/dash0/email-preview` shows a link that actually resolves.

3. **Pin it with a test.** Add a unit test on the auth service (same package
   as `membership_requests.go`) that triggers a membership request against an
   org with a known slug, captures the enqueued email view model, and asserts
   `RequestsURL == "<BaseURL>/dash0/orgs/<slug>/organization/requests"`. The
   assertion must be on the exact path, not a `Contains("requests")`, so a
   future route rename fails loudly here rather than silently in an inbox.
   If there is a lightweight way to cross-check against the dash0 route table
   (`routeTree.gen.ts` lists `/orgs/$org/organization/requests`), a comment
   pointing at it is enough — do not add a Go→TS build dependency.

4. **Never render an empty org name.** When `org.Name` is blank, fall back to
   the slug (`default`) before calling `ApplyOrgBranding`, so both the subject
   and the two body sentences stay grammatical. Do this once in the notifier
   (or in `ApplyOrgBranding` itself if the other callers benefit — check the
   decision email at
   [server/internal/handlers/auth/membership_requests.go:419](server/internal/handlers/auth/membership_requests.go:419)
   and the welcome/invitation templates, which take the same branding path).
   Cover it with a table-driven test: name set → name used; name empty → slug
   used; template output contains neither `join  on` nor `admin of .`.

### Out of scope

- Fixing the empty `name` on the production `default` org row itself. That is
  data, not code; note it in the PR description so it can be patched by hand.
- Any redesign of the requests page or the members page.

### Open question

- Should the members page honour `?tab=requests` as a redirect to
  `/organization/requests` so links already sitting in admins' inboxes keep
  working? Emails sent before this fix ships will stay broken otherwise. A
  small `beforeLoad` redirect on `organization.members.tsx` is cheap; include
  it if it stays under ~10 lines, otherwise leave it out and say so.
