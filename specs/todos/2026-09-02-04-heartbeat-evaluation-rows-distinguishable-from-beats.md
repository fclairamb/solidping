---
model: opus
effort: high
---

# Heartbeat evaluation rows are indistinguishable from beats, so users open one and think the caller was not recorded

Depends on 2026-09-02-03 (the evaluation must be reading a real beat for the
"open the signal" link below to point at one).

## Problem

Heartbeat and email checks write two kinds of raw rows that both read
"Heartbeat received" / status Up:

| | Signal row (beat) | Evaluation row |
|---|---|---|
| written by | `recordBeat` (`server/internal/handlers/heartbeat/service.go:289`), email ingest (`emailcheck/handler.go:~418`) | `executePassiveJob` (`server/internal/checkworker/worker.go:1488`), every period, from a checks worker |
| `worker_uid` / `region` | none | worker + its region |
| output | `message`, `userAgent`, `remoteAddr`, `httpMethod`, `data` (`buildHeartbeatOutput`, `service.go:67`) | `message`, `lastSignalAt` (+ `overdueBy` / `runStarted`) |
| Up message | `Heartbeat received` (`defaultOutputMessage`, `service.go:44`) | `Heartbeat received` (`worker.go:1509`) |

In dash0 the Recent Results table
(`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1862-1930`)
shows Time / Status / Duration / Region, so the only visible difference is
that evaluation rows have a region. The result detail page renders the
"Caller" and "Data" cards only from the ingest keys
(`checks.$checkUid.results.$resultUid.tsx:145-155, 339-380`); an evaluation
row falls through to the raw "Output" JSON dump
(`{"message":"Heartbeat received","lastSignalAt":"…"}`) with no explanation.

So a user who opens the evaluation row written 8 s after their ping (dev
example: beat `01a0621f-10fd-…` at 12:36:38Z, evaluation `01a0621f-2f86-…`
at 12:36:46Z on check `1893d40c-…`) sees status Up, "Heartbeat received",
no Caller card — and concludes the caller was not recorded. The same
ambiguity reaches API and MCP consumers reading `output` (`list_results`,
`diagnose_check`), the check-header "Output" block (`index.tsx:1689`, which
shows `lastResult.output` raw) and incident snapshots.

## Proposal

Evaluation rows declare themselves in their output; beat rows do not change
at all. dash0 keys everything off that declaration.

### D1. Evaluation output marks itself (worker only)

In `executePassiveJob`, every branch's output gains:

| key | value | when |
|---|---|---|
| `evaluation` | `true` | always |
| `lastSignalAt` | RFC3339 of the beat | whenever a signal exists (already present in the Up/overdue branches; add to the running and default branches) |
| `lastSignalResultUid` | the beat's result uid | whenever a signal exists — the worker already holds the row |
| `overdueBy`, `runStarted` | unchanged | as today |

Only the **Up** message changes, because it is the only one that collides
with the ingest wording:

| branch | today | after |
|---|---|---|
| Up, on time | `Heartbeat received` | `Heartbeat on time` (`Email on time`) |
| Up but overdue | `Heartbeat overdue` | unchanged |
| newest signal is a `down` / `error` beat | `No heartbeat received` | `Last heartbeat reported failure` / `… reported error` (the beat *was* received; the current text is false) |
| no signal at all | `No heartbeat received` | unchanged |
| running / stale run | unchanged | unchanged |

Add the key names as constants next to `outputKeyMessage`
(`worker.go:78`). `recordBeat`, `buildHeartbeatOutput`,
`defaultOutputMessage` and the email ingest are **not touched** — what is
stored for a beat is byte-for-byte what it is today, and the tests below
prove it.

No API schema change: `output` is free-form (`openapi.yaml:10499`); add one
sentence to its description pointing at the docs paragraph in D7.

### D2. Result detail page: an "Evaluation" card instead of a JSON dump

New `web/dash0/src/components/checks/evaluation-card.tsx`, exporting
`EvaluationCard` and `EVALUATION_OUTPUT_KEYS` (the DnsblCard /
EmailDeliveryCard convention, `results.$resultUid.tsx:16-17, 162-168`).

Rendered when `output.evaluation === true`. Legacy fallback for rows written
before deploy: `lastSignalAt` or `runStarted` present **and** none of the
caller keys — those rows age out with raw retention (24 h default), so the
fallback is a courtesy, not a contract.

Card content (`data-testid="evaluation-card"`):

- title **Scheduler evaluation**; first line is the row's `message`;
- explainer: *"This row was written by the {region} checks worker when it
  evaluated the check's schedule. It is not a heartbeat — nothing called in
  at this time."* (email wording: *"…no email arrived at this time."*);
  region via `regionDisplayLabel`, omitted when the row has none;
- **Last signal**: `lastSignalAt` formatted like the Period card, followed
  by *"{{duration}} before this evaluation"* computed as
  `periodStart − lastSignalAt` (reuse the duration formatter behind
  `checks:detail.summary.ago`); **Overdue by** `overdueBy` and **Run
  started** `runStarted` when present; *"No signal on record"* when there
  is neither;
- actions: **Open the signal** (`data-testid="evaluation-open-signal"`) →
  `/orgs/$org/checks/$checkUid/results/$lastSignalResultUid` **with
  `search: { region: undefined }`** — the beat has no region, so carrying
  the evaluation's `?region=` would scope its prev/next neighbours to a
  region it is not in; and **View all results for this check** (existing
  `resultDetail.viewAll` key) → the check page.

Header: `<Badge variant="secondary">Evaluation</Badge>` after the status and
`periodType` badges (`results.$resultUid.tsx:190-200`).

The keys in `EVALUATION_OUTPUT_KEYS` (`evaluation`, `lastSignalAt`,
`lastSignalResultUid`, `overdueBy`, `runStarted`) **and `message`** are
stripped from the raw Output dump for evaluation rows, so the Output card
disappears when nothing else remains. Beat rows keep today's rendering
exactly; give the existing Caller card `data-testid="caller-card"`.

### D3. Recent Results table: a muted "Evaluation" badge

On the check page, for passive check types only (`check.type` is
`heartbeat` or `email` — never widen the request for other types), the
10-row Recent Results query (`index.tsx:940-946`) asks for
`with: "durationMs,region,output"`. The chart-window query is unchanged.

Rows with `output.evaluation === true`:

- Status cell: `<StatusBadge>` followed by
  `<Badge variant="outline" className="text-muted-foreground">Evaluation</Badge>`
  (`data-testid="result-evaluation-badge-{uid}"`), wrapped in a Tooltip:
  *"Written by the scheduler when it evaluated the check. Not a heartbeat."*
- Time cell in `text-muted-foreground`.

Row click, region badge and everything else unchanged. On mobile the badge
wraps under the status badge; no fixed widths.

### D4. Check header "Output" block

`index.tsx:1689-1712` lists `lastResult.output` as raw key/value pairs;
filter out `evaluation` and `lastSignalResultUid` the way
`IP_VERSION_OUTPUT_KEY` is, so the block reads `message` + `lastSignalAt`
rather than `evaluation: true`.

### D5. Design reference

`web/dash0/src/routes/orgs/$org/design-reference.tsx` — mandatory per
CLAUDE.md when a pattern is new. In the Buttons & Badges section
(`:1335`), add a **Row-kind badge** example: the outline + muted "Evaluation"
badge next to a `StatusBadge`, with the note *"for rows the system wrote on
its own schedule; sits after the status badge, never uses a status
colour."* Add `EvaluationCard` to the cards catalogue with its import line.

### D6. Locales (de, en, es, fr — `web/dash0/src/locales/*/checks.json`)

English strings, to be translated in the other three:

```json
"resultDetail": {
  "evaluation": {
    "badge": "Evaluation",
    "title": "Scheduler evaluation",
    "explainerHeartbeat": "This row was written by the {{region}} checks worker when it evaluated the check's schedule. It is not a heartbeat — nothing called in at this time.",
    "explainerEmail": "This row was written by the {{region}} checks worker when it evaluated the check's schedule. It is not an email — no email arrived at this time.",
    "explainerNoRegion": "This row was written by a checks worker when it evaluated the check's schedule. It is not a heartbeat — nothing called in at this time.",
    "lastSignal": "Last signal",
    "before": "{{duration}} before this evaluation",
    "overdueBy": "Overdue by",
    "runStarted": "Run started",
    "noSignal": "No signal on record",
    "openSignal": "Open the signal"
  }
},
"detail": {
  "results": {
    "evaluationBadge": "Evaluation",
    "evaluationTooltip": "Written by the scheduler when it evaluated the check. Not a heartbeat."
  }
}
```

`bun run test:unit` must stay green (it includes the locale-key parity
checks).

### D7. Docs

`web/docs/docs/features/check-types.md`, Heartbeat section (`:1004`): a
short **"Two kinds of result rows"** paragraph — beats (caller metadata,
no region) versus scheduler evaluations (region, `evaluation: true`,
`lastSignalAt`, `lastSignalResultUid`, `overdueBy`, `runStarted`), the
message table from D1, and that a beat older than raw retention reads "No
heartbeat received". API and MCP consumers read `output`, so the keys are
part of the documented surface. Mirror the paragraph in the email-check
section if it has a results subsection.

### Tests

Backend (`server/internal/checkworker/worker_test.go`, alongside
`TestExecuteHeartbeatJob_RunningStatus` at `:756`):

- `TestExecutePassiveJob_MarksEvaluationRows`, table over heartbeat and
  email: seed a beat row (`WorkerUID: nil`, output from a
  `buildHeartbeatOutput`-shaped map with `userAgent`, `remoteAddr`,
  `httpMethod`, `data`), run `executePassiveJob`, assert the new row has
  `WorkerUID != nil`, `evaluation == true`, message `Heartbeat on time` /
  `Email on time`, `lastSignalAt ==` beat `period_start` (RFC3339),
  `lastSignalResultUid ==` beat uid. Then re-read the beat row and assert
  its `Output` is deep-equal to what was inserted — no `evaluation` key, all
  caller keys intact. Cover the overdue, no-signal and running branches for
  `evaluation == true` and the D1 message table.
- `server/internal/handlers/heartbeat/service_test.go`: one assertion added
  to an existing ingest test that the stored output has no `evaluation` key
  (positive control that ingest never marks itself).

Playwright (`web/dash0/e2e/check-heartbeat-evaluation-rows.spec.ts`):

1. **Real flow, no mocks** (test mode): create a heartbeat check through the
   API with period `01:00:00` (helper from `live-updates.spec.ts:28-60`;
   move it to `fixtures.ts` if a second file needs it), poll
   `/results?checkUid=…&with=output` until the creation-time evaluation row
   exists (`output.evaluation === true`, status down — it lands ~1 s after
   creation, see `live-updates.spec.ts:100-115`), **then** send one beat via
   `GET /api/v1/heartbeat/test/{uid}?token=…` and poll until a row with
   `remoteAddr` exists. With a 1 h period no further evaluation can run, so
   the table holds exactly one of each. Assert: exactly one
   `result-evaluation-badge-*`; clicking that row shows `evaluation-card`,
   "No signal on record", and no `caller-card`; back; clicking the other row
   shows `caller-card` with a Source IP and no `evaluation-card` / badge.
2. **Mocked detail rendering** (convention of
   `check-result-detail-navigation.spec.ts`): fixtures for an evaluation row
   (`region: "eu"`, output `{message: "Heartbeat on time", evaluation: true,
   lastSignalAt, lastSignalResultUid: <beat uid>}`) and the beat row
   (caller keys). Open the evaluation detail with `?region=eu`: card shows
   the last-signal time and "… before this evaluation", header carries the
   Evaluation badge, no Output card; click **Open the signal** → lands on
   the beat detail, URL has **no** `region` param, Caller card visible.

Gates: `make lint`, `make test` for `checkworker` and `handlers/heartbeat`,
`bun run test:unit`, the new e2e file, and the full dash0 e2e suite as the
batch gate.

### Out of scope / follow-ups

- A "beats only" filter chip on the Recent Results table.
- Reusing `EvaluationCard` on the incident detail page for the opening
  result snapshot of a heartbeat incident.
- Whether evaluation rows should count toward availability.
