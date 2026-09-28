---
effort: medium
---

# The events page cannot say which agent connected, where, or why it disconnected

## Problem

`agent.connected` / `agent.disconnected` rows (spec `2026-09-25-05`) render as:

```
29s ago | Agent Connected    | System | (nothing)
29s ago | Agent Disconnected | System | (nothing)
3m ago  | Agent Connected    | System | (nothing)
3m ago  | Agent Disconnected | System | (nothing)
```

The label is identical for every agent, Actor is always `System`, and the
Related column is empty — so a page full of them (org `stonal` on
solidping.k8xp.com had **158 connects / 146 disconnects in 25 h**, 144 of them
from a single agent) never answers the three questions a reader actually has:
*which* agent, *which* private location, and *why* it dropped.

The data is already recorded and already returned; dash0 simply never reads it:

- `runAgentConnection` calls `audit.Record` with
  `audit.Target{Type: "agent", UID: agent.UID, Name: agent.Name}`
  ([handler.go:717-729](server/internal/handlers/agentws/handler.go#L717)),
  and `buildPayload` copies `target_type` / `target_uid` / `target_name` into
  the payload next to the caller's `region` and `reason`
  ([audit.go:284-300](server/internal/audit/audit.go#L284)).
- Payload keys are the exported constants
  `models.AgentEventPayloadRegion` / `AgentEventPayloadReason` and the four
  `AgentDisconnectReason*` values
  ([event.go:275-293](server/internal/db/models/event.go#L275)).
- The list API returns `payload` verbatim, no redaction, no role gate
  (`EventResponse.Payload`,
  [events/service.go:100](server/internal/handlers/events/service.go#L100), copied
  at `:230`).

The Related column today only ever renders `checkUid` / `incidentUid` links
([event-log-table.tsx:176-206](web/dash0/src/components/dashboard/event-log-table.tsx#L176)),
and the one "second line under the label" helper, `getActivationDetail`, is
hard-wired to `org.activation.first_notification_configured`
(`:44-81`). Payload-derived *values* do have a precedent — `getEventCheckName`
reads `payload.check_name` — so this spec follows that pattern rather than
inventing a new one.

This is not cosmetic. Reading "why" is what turned the flapping reported in
`/d/orgs/stonal/events` into a diagnosis: **142 of the 146 disconnects carry
`reason: "ping_timeout"`**, which is a server-declared keepalive failure, not a
dead agent (root cause and fix in spec `2026-09-28-03`). Today that fact is
reachable only with `psql` against the deployment's database.

## Decisions

- **Render what is already in the payload.** No backend change: no new payload
  keys, no new columns, no migration, no API change. Existing rows (all of
  them) light up retroactively.
- **Actor stays `System`.** The agent is not a user; the agent is the *subject*
  of the event, which is what the Related column is for (same as a check).
- **The event type / label is unchanged.** The type is the filter key, the
  locale key and the identity of the row; the name goes on a second line, not
  into `types.agent.connected`.
- **Name first, region second, reason last**: `74782dfcd3e5 · @aws-paris ·
  ping timeout`. The name is what the org picked at enrollment (or the agent's
  hostname when it did not pick one), the region is what makes it locatable in
  the Private Locations page, the reason only exists on disconnects.
- Out of scope: renaming an agent from the events page, an agent detail page,
  and a `targetUid=`-style deep link from the row into a filtered view — the
  API already supports `?targetType=agent`, but nothing else needs it yet.

## Proposal

### 1. Payload getters — `web/dash0/src/components/dashboard/event-display.tsx`

Next to `getEventCheckName` (`:462`), three pure getters, all
`string | undefined` and all returning `undefined` when the key is missing so
callers can skip the line entirely (historical rows, other event types):

- `getEventAgentName(event)` → `payload.target_name`
- `getEventAgentRegion(event)` → `payload.region`
- `getEventDisconnectReason(event)` → `payload.reason`, narrowed to the four
  `AgentDisconnectReason*` values; anything unknown returns `undefined` rather
  than a raw machine code (the row must never render `ping_timeout` untranslated).

### 2. Rendering — `web/dash0/src/components/dashboard/event-log-table.tsx`

- Generalise `getActivationDetail` into a single `getEventDetail(event, org, t)`
  that keeps the existing activation branch and adds the agent branch: a muted,
  `pl-6` line under the label (same indent/typography rules as today) reading
  `name · region` and, for `agent.disconnected`, ` · <translated reason>`.
  The existing call site (`:117-127`) and its "skip when null" contract are
  unchanged, so every other event type renders exactly as before.
- In the Related cell, for `agent.connected` / `agent.disconnected`, render the
  same `inline-flex items-center gap-1.5 text-xs …` chip the check link uses,
  with the `Plug` / `Unplug` icons `event-display.tsx` already registers for
  these two types (export a small `getEventIcon`-style helper rather than
  importing lucide into a second file) and the agent name as its label, linking to
  `/orgs/$org/organization/private-locations/` (route exists:
  `organization.private-locations.index.tsx:70`). When `target_name` is absent,
  fall back to the region; when both are absent, render nothing (today's
  behaviour).
- The dashboard's "Recent activity" card uses this same component (spec
  `2026-09-25-32`), so it inherits the change with no extra work — which is the
  point of that spec: one rendering of an events table.

### 3. Strings — `web/dash0/src/locales/{en,fr,de,es}/events.json`

- `links.privateLocation` (or similar) for the Related chip.
- `disconnectReasons.{ping_timeout,revoked,server_shutdown,error}` — four keys,
  four locales, mirroring the human strings the server-side evaluator already
  uses
  ([private_location_evaluation.go:257-260](server/internal/checkworker/private_location_evaluation.go#L257):
  "ping timeout", "revoked", "server shutdown", "error").
- No new top-level namespace; both live under the existing `events` namespace.
  `locale-parity.test.ts` enforces key parity across the four locales and
  `untranslated-values.test.ts` catches an untranslated copy-paste.

### 4. Docs — `web/docs/docs/features/events.md`

One short paragraph under "Event types" saying agent rows carry the agent
name, its private location and the disconnect reason, and that the Related
chip opens the Private Locations page. Nothing else in the doc changes.

## Tests

- `web/dash0/src/components/dashboard/event-display.test.ts`: table cases for
  the three getters — full payload, payload without `target_name` (region-only
  row), unknown reason value, and a non-agent event (all `undefined`).
- A rendering-level test for `EventLogTable` if the existing dash0 test setup
  has a DOM harness for it (it currently exercises `event-display` only); if
  not, the getter tests plus the existing Playwright events coverage are the
  accepted floor — the spec does not introduce a new test framework.
- `make lint` / `make test-dash` green; locale parity tests green without
  allowlist edits (no new untranslated keys).

## Acceptance

On `/d/orgs/<org>/events`, an `agent.disconnected` row reads

```
29s ago | Agent Disconnected            | System | ⑂ 74782dfcd3e5
         └ 74782dfcd3e5 · @aws-paris · ping timeout        ← muted second line
```

with the chip linking to the Private Locations page, in all four locales, and
an `agent.connected` row showing the same identity without the reason
(`74782dfcd3e5 · @aws-paris`).
