---
model: opus
effort: medium
---

# A `private` status page cannot be viewed by the signed-in members of its own organization

GitHub Issue: https://github.com/fclairamb/solidping/issues/1072

## Source

An external self-hosted user (v0.56.0, Docker, PostgreSQL) sets a status page to
`visibility: private`, signs in to the dashboard, clicks "View" on the page, and gets the
status0 "Status Page Not Found / The requested status page could not be found." screen. A kiosk
(TV mode) token works, `public` works. They expected a signed-in member to see a `private` page.

Side remark in the same report: on the status page edit screen, the Cancel / Save buttons sit in
the middle of the page instead of at the bottom.

The report is right on the facts and describes behavior that is **by design today**: `private`
means "hidden entirely, the public endpoints answer 404". The expectation (members can view it)
is a reasonable product reading of the word "private" that the code does not implement.

## Problem

- `server/internal/statuspagelock/statuspagelock.go:88-101` (`Visible`): only `public` and a
  hashed `password` page are servable; `private` falls into `default: return false`.
- `server/internal/statuspagekiosk/statuspagekiosk.go:185-205` (`Decide`): the single gate of
  every public surface. Order: disabled → 404, valid kiosk token → allow, `!Visible` → 404,
  locked → 401. There is no branch for an authenticated caller of the page's organization.
- `server/internal/handlers/statuspages/service.go:2590` (`publicAccessError`) maps that decision
  for `ViewStatusPage` / `ViewStatusPageSummary`; the same gate is reached from the badge, the
  feed, the public incidents list, the subscriber endpoints
  (`handlers/statussubscribers/handler.go:438`, `service.go:279`) and custom-domain routing
  (`app/custom_domain_routing.go:289`).
- `server/internal/app/server.go:2292-2311`: these routes are mounted on `publicOrgAPI` with no
  auth middleware, so even a request that carries the dashboard's `access_token` cookie
  (`middleware/auth.go:32`, accepted as a fallback by `extractToken`, `auth.go:666`) never gets a
  user in its context.
- The dashboard "View" button links to `${STATUS_BASE}/${org}/${slug}`
  (`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx:1387`), i.e. the public
  status0 app, which renders the 404 as "Status Page Not Found".
- Caching: `statuspagecache` already serves `private` as `private, no-store`
  (`statuspagecache/statuspagecache.go:15`), so a member-only body would not be retained by a
  shared cache; the responses would also need `Vary: Cookie, Authorization`.

Side remark: `web/dash0/src/components/shared/status-page-form.tsx:643-651` renders the
Cancel / Save row at the end of the `<form>`, but the edit route
(`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.edit.tsx:60-129`) then stacks four
more cards below it (appearance link, branding, custom domain, subscribers), which have their own
save actions. The form's buttons therefore land mid-page.

## Why a spec

- A changed API behavior on every public status page endpoint (who may read a `private` page),
  plus OpenAPI and doc text that currently promise a 404.
- Correctness hinges on authorization scope: membership in the page's organization, the
  `access_token` cookie on a public route (CSRF is irrelevant for GET, but token validation,
  impersonation, forced rotation and the "invalid cookie → 401" behavior of `AuthIfPresented`
  must not turn the public endpoints into an oracle or break anonymous visitors).
- A product decision the reporter did not settle: what `private` means, and whether custom
  domains participate.

## Proposal

1. **Gate.** Add a member branch to `statuspagekiosk.Decide`, after the kiosk check and before
   `Visible`: if the request context carries an authenticated user (or a PAT / API key) whose
   organization membership includes `page.OrganizationUID` and who has read access to status
   pages, return `DecisionAllow` for a `private` page **and** for a `password` page (a member
   should not have to type the password of their own page). Non-members, anonymous callers and
   invalid tokens keep today's byte-identical 404 / 401.
2. **Auth on the public routes.** Wrap the public status page GET routes
   (`server.go:2292-2311`, plus the feed / badge / incidents siblings) in a soft authentication
   that never fails: when a credential is present and valid, put the user in the context; when it
   is absent **or invalid**, continue as anonymous (do not reuse `AuthIfPresented` as is: its
   401 on a stale cookie would break anonymous visitors of public pages who once signed in on the
   same host). Only the `access_token` cookie and the `Authorization` header count.
3. **Caching.** Any response served through the member branch carries `Cache-Control: private,
   no-store`; a `public` page keeps its current public caching whoever asks. Add
   `Vary: Cookie, Authorization` on gated responses.
4. **status0.** Its fetches to the page API must send the cookie (`credentials: "same-origin"`
   where the API is same-origin; check how `STATUS_BASE` resolves on self-hosted and on
   solidping.io). For a `private` page viewed by a member, show a small "Private: only visible to
   members of <org>" banner (`data-testid="status-page-private-banner"`).
5. **Custom domains.** The dashboard cookie is host-only on the app host, so a custom domain
   never carries it: `private` pages on a custom domain keep answering 404 (see open questions).
6. **Edit page layout.** Move the four extra cards of the edit route above the form's action
   row, or make the form's Cancel / Save row sticky at the bottom of the viewport
   (`sticky bottom-0 bg-background border-t py-3`, `data-testid="status-page-form-actions"`).
7. **Docs.** Update the visibility descriptions in the OpenAPI spec (regenerate
   `server/pkg/client`), `models/status_page.go:56`, the `statuspagelock` package doc, the MCP
   tool descriptions (`internal/mcp/tools_statuspages.go:187,268,737`), the CLI flag usage and the
   dashboard visibility hint (`status-page-form.tsx:395`) to say "private: visible only to signed-in
   members of the organization".

**Tests.**
- Go, table-driven on `Decide` and on `ViewStatusPage` / summary / badge / feed: `private` page ×
  {anonymous, member cookie, member bearer, member of another org, invalid cookie, revoked kiosk
  token} → {404, 200, 200, 404, 404 (not 401), 404}; `password` page × member → 200 without unlock;
  member responses carry `private, no-store`.
- Playwright: sign in, create a `private` page, open "View", see the page and the private banner;
  sign out, same URL → "Status Page Not Found".
- Playwright: on the edit page, the Save button is visible without scrolling past the subscribers
  card (or sits below the last card).

## Open questions

1. **Does a signed-in member also bypass the `password` gate?** Recommended: yes, same as the
   kiosk token; a member already has admin read access to the page. Trade-off: a member cannot
   check "what a visitor sees" without signing out (the dashboard preview covers that).
2. **Which members?** Recommended: any member of the page's organization with read access to
   status pages (every role today). Trade-off: a finer "viewer list" per page is a later feature.
3. **Custom domains?** Recommended: no member access there; `private` stays 404 on a custom
   domain, since the dashboard cookie never reaches it and minting a cross-host session is a much
   larger change. Trade-off: a team that wants an internal page must use the app host URL.
4. **Edit page layout: sticky action row or reorder the cards?** Recommended: sticky action row,
   so the save stays reachable whatever is added below. Trade-off: it takes a strip of the
   viewport on small screens.

## Closing the issue

This spec closes #1072. The implementing PR body **must** carry a `Closes #1072` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is closed
and, if the merge did not close it, close it by hand:

    gh issue close 1072 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
