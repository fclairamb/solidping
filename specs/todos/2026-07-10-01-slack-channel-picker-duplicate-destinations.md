# Slack channel picker lists every channel dozens of times (duplicate destinations)

## Problem

The Slack destination picker (Channel / Direct message dropdown on the
integration settings page) shows every channel repeated many times — e.g.
`#ai-veille` appears over and over, each row carrying the same
"Bot not in channel — run /invite @solidping first" warning.

Captured in `integrations.priv.har`, the response of
`GET /api/v1/orgs/:org/channels/:uid/slack/destinations` contains **2500
channel entries for only 100 distinct channels — each channel repeated exactly
25 times**:

```
distinct channels: 100   total entries: 2500   (per channel: 25×)
users: 196 distinct = 196 total  (NOT duplicated)
```

25 is exactly `maxListPages` ([`client.go:44`](server/internal/integrations/slack/client.go)).
So the single 100-channel page of `conversations.list` is being fetched 25
times in a row, until the page cap stops the loop. After
`GetDestinations` sorts the list by name
([`service.go:1113`](server/internal/integrations/slack/service.go)), the 25
copies of each channel cluster together, which is what the picker renders.

### Root cause

`paginate` ([`client.go:346`](server/internal/integrations/slack/client.go))
loops until Slack returns an empty `next_cursor` or the page cap is hit. It has
**no guard against a cursor that does not advance**:

```go
for range maxListPages {
    next, err := fetchPage(cursor)
    ...
    if next == "" { return nil }
    cursor = next          // blindly trusts Slack; never checks next == cursor
}
```

`conversations.list` here returns a non-empty `next_cursor` that yields the same
page again (a known Slack quirk on `conversations.list`, and/or a stable cursor
on the final page), so the same 100 channels are appended 25 times.
`users.list` doesn't exhibit it, which is why users come back clean — the defect
only bites when Slack hands back a repeating/non-advancing cursor.

There is also **no de-duplication by ID** in `ListChannels`
([`client.go:373`](server/internal/integrations/slack/client.go)) or in
`GetDestinations`, so nothing downstream catches the repeats.

## Proposal

Fix in `paginate` (covers both channels and users) plus a defensive de-dup:

1. **Stop when the cursor does not advance.** In `paginate`, after getting
   `next`, break the loop if `next == cursor` (or if `next` has already been
   seen). A non-advancing cursor means no forward progress — continuing only
   re-fetches the same page. This is the core fix.

2. **De-duplicate by ID defensively.** In `ListChannels` (and, symmetrically,
   `ListUsers`), drop entries whose Slack ID was already collected, so a buggy
   cursor or overlapping pages can never surface duplicates in the picker even
   if the guard above is bypassed.

3. **Tests.** Add a table-driven test for `paginate` / `ListChannels` where the
   fake Slack client returns a **constant non-empty `next_cursor`** with the
   same page each call, and assert the result contains each channel exactly
   once (not `maxListPages` copies). Guard the "cursor repeats immediately"
   and "cursor repeats after N pages" cases.

### Open question

We only have the aggregated `/destinations` response in the HAR, not the raw
`conversations.list` calls, so whether Slack returns the *identical* cursor
string or a *different-but-equivalent* one each time isn't 100% certain. The
`next == cursor` guard handles the identical-string case; the seen-cursor set
plus ID de-dup (step 2) covers the different-string case. Implement both so the
fix is robust either way.
