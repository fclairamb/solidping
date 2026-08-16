# Slack channel picker can't find existing channels — `conversations.list` / `users.list` are not paginated

## Problem

On the integration edit page (observed on
`https://solidping.k8xp.com/dash0/orgs/acmetech/integrations/b69c17d8-…`),
searching the "Pick a channel…" combobox for `#solidping-dev` — a channel
that exists and that the bot is a member of — returns "No channels found".
The channel can never be selected.

### Diagnosis

The picker is fed by `GET /api/v1/orgs/:org/channels/:uid/slack/destinations`
(`server/internal/app/server.go:1100`), which calls
`Service.GetDestinations` (`server/internal/integrations/slack/service.go:1012`)
→ `client.ListChannels` + `client.ListUsers`.

`ListChannels` (`server/internal/integrations/slack/client.go:318`) makes a
**single** `conversations.list` call:

```go
c.callAPI(ctx, "conversations.list", map[string]any{
    "types":            "public_channel,private_channel",
    "exclude_archived": true,
}, &result)
```

No `limit`, no `cursor`. Slack's `conversations.list` returns **100 channels
per page by default** and signals more via
`response_metadata.next_cursor`; ignoring the cursor silently drops every
channel past the first page. Channels come back in roughly creation order,
so in a workspace with hundreds of channels (the Acme workspace easily
qualifies) a recently created channel like `#solidping-dev` is effectively
never in the first 100.

The dash0 combobox filters **client-side** over that truncated array
(`web/dash0/src/components/integrations/integration-form.tsx:1235`), so
typing `solid` matches nothing → "No channels found"
(integration-form.tsx:1279).

Evidence this is truncation and not an auth/scope/membership issue:

- The combobox rendered with a search box, i.e. the destinations call
  succeeded and returned a non-empty list (a failed call renders the error
  state, an empty list renders the "Invite the bot" hint — neither shown).
- `conversations.list` returns **all public channels regardless of bot
  membership** (the UI even renders a "Bot not in channel" hint off
  `is_member`), so a missing public channel means it wasn't in the response
  at all. If `#solidping-dev` is private, a missing `groups:read` scope
  would have failed the whole call — not returned a partial list.

Two more victims of the same bug:

- `ListUsers` (client.go:335) calls `users.list` unpaginated — for large
  workspaces Slack truncates and returns a cursor, so the "Direct message"
  tab is missing users too.
- `SetDefaultChannel` (service.go:911) resolves the channel display name
  by scanning the same truncated list — for channels past page 1 it stores
  an empty `channelName` (cosmetic; fixed for free by fixing the client).

## Proposal

### A. Cursor pagination in the Slack client

In `client.go`, make `ListChannels` and `ListUsers` loop:

- pass `limit: 200` (Slack's recommended page size; both methods are
  rate-limit Tier 2, ~20 req/min, so even multi-thousand-channel
  workspaces complete a fetch comfortably);
- read `response_metadata.next_cursor` from each response and pass it as
  `cursor` on the next call, until the cursor comes back empty;
- aggregate pages; keep the existing per-page filtering for `users.list`
  (skip bots and deleted users);
- safety cap at 25 pages (5 000 items) per method — if hit, log a warning
  and return what we have rather than looping forever.

Response structs gain:

```go
ResponseMetadata struct {
    NextCursor string `json:"next_cursor"`
} `json:"response_metadata"`
```

While in `GetDestinations`, sort channels by name and users by display
name (case-insensitive) before returning — Slack's order is arbitrary and
the picker currently mirrors it.

### B. Make the client testable, add pagination tests

`Client` hardcodes the `SlackAPIBaseURL` const and there is no
`client_test.go`. Add a `baseURL` field on `Client` defaulting to the
const, with a test-only override (small constructor variant or option,
mirroring how `socketmode.go` uses an injectable factory for httptest).

New `client_test.go` against an `httptest.Server` fake Slack:

- serves `conversations.list` in 3 pages keyed off the `cursor` param;
  asserts every page's channels are in the result — in particular a
  channel that only exists on the last page (the regression case);
- asserts `limit` is sent, the cursor chain terminates on empty cursor,
  and the page cap stops a server that always returns a cursor;
- same for `users.list`, including bot/deleted filtering across pages.

### C. Frontend

No change required — once the backend returns the full list, the existing
client-side filter finds the channel. (Payload stays reasonable: even
2 000 channels is a few hundred KB fetched once per picker open.)

## Out of scope

- Server-side search / a `q` param on the destinations endpoint, and
  virtualized rendering of huge lists — unnecessary below the 5 000-item
  cap; revisit if a real workspace hits the cap warning.
- Caching channel lists between picker opens.
- Retry/backoff on Slack `ratelimited` errors — a single fetch stays well
  under Tier 2 limits at 200/page.
- The deprecated `web/dash` app.

## Acceptance criteria

- Unit tests: a channel present only on page 3 of a fake 3-page
  `conversations.list` is returned by `ListChannels`; same shape for
  `ListUsers`; cap behavior covered.
- `GET /api/v1/orgs/:org/channels/:uid/slack/destinations` returns
  channels sorted by name, users sorted by display name.
- `make test` and `make lint` green.
- Manual check after deploy to `solidping.k8xp.com`: on the Acme
  integration's edit page, typing `solid` in the channel picker lists
  `#solidping-dev` and selecting it saves correctly.

## Implementation plan

- [ ] A: cursor pagination + page cap in `ListChannels` / `ListUsers`,
      `ResponseMetadata` on response structs, sorting in `GetDestinations`.
- [ ] B: injectable base URL on `Client`; `client_test.go` with multi-page
      httptest fake covering channels, users, filtering, and the cap.
- [ ] Verify: `make test`, `make lint`; manual picker check on the k8xp
      dev deployment once released.
