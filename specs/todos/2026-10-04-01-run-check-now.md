---
model: sonnet
effort: medium
---

# Any check can be run once on demand ("Run now")

## Problem

After fixing an outage, the user waits up to a full period for the check to confirm it. Only browser/js/rdp/vnc checks have an on-demand run, and it is tied to screenshots: "Capture now" (spec 2026-09-25-34, `server/internal/handlers/checkscreenshots/service.go:245`). It makes one job row due, then sends the express hint.

That spec built the run-now path but kept it screenshot-only. Spec 2026-05-05-03 (activation time-to-first-signal, line 52) also asked for a "run now" hook so a new check gives its first result at once.

## Proposal

1. **Shared run-now service.** Move `pickJob` (`checkscreenshots/service.go:299`), `compareRegion` (`:318`), `hint` (`:340`) and the `AdmitFixedWindows` wrapper `admit` (`:360`) into a new package `server/internal/handlers/checkrunnow/`. Its service exposes:
   - `RunNow(ctx, orgSlug, identifier) (*RunNowResponse, error)`
   - the helpers `checkscreenshots` keeps using.

   "Capture now" then calls the shared helpers with its own windows. Its behaviour and its `capture-now.*` keys are unchanged.

2. **Make every region due.** Add `db.Service.RequestCheckRun(ctx, jobUID, requestedAt)` next to `RequestCheckCapture` (`server/internal/db/service.go:449`). It sets `scheduled_at` and `effective_scheduled_at` to `requestedAt`, without the capture flag. Implement it in both `server/internal/db/postgres/postgres.go:2315` and `server/internal/db/sqlite/sqlite.go:2218` (copy the `RequestCheckCapture` shape).
   - `RunNow` calls it on every job row of the check that is not running.
   - A job is running when it is leased, or when it carries a multi-step run in progress (`StepRunUID != nil`, the test `checkruns.GetRun` uses at `server/internal/handlers/checkruns/service.go:81`). Its result answers the request. It is reported as `running`, not re-queued: making a crawl's job due would only hurry its next slice, not start a fresh run.
   - Then send one express hint.
   - Once the run is claimed, the release schedules the next run from the regular period, as for any run. The schedule is not shifted further.

3. **Response.** `RunNowResponse`:
   - `{ "requestedAt": <µs-truncated time>, "regions": [{ "region": "eu", "status": "queued" | "running" }] }`
   - Errors:
     - `404 NOT_FOUND` for an unknown check.
     - `409 CONFLICT` for a disabled check, or when there is no job row (reuse the `ErrNoScheduledJob` semantics).
     - `429` with `Retry-After` from the rate limiter (reuse `RateLimitedError`, `checkscreenshots/service.go:72`).
   - Validation runs before admission, so a refused request spends no budget.

4. **Rate limits.** Add separate windows keyed `run-now.check.<uid>` and `run-now.org`.
   - Cheap types: 3 per check per minute, 60 per org per hour.
   - `browser`, `js`, `rdp`, `vnc` (`captureTypes`, `checkscreenshots/service.go:88`) use the capture limits (`:41-44`): 1 per check per minute, 20 per org per hour.

5. **Incidents.** The result goes through the normal result path, so it counts like any scheduled result. It can open or resolve an incident, and time-based confirmation/recovery periods still apply. No special-casing.

6. **Route.** Register `POST /api/v1/orgs/:org/checks/:checkUid/run-now` through `orgGroup` (`server/internal/app/server.go:1428`, the `screenshots/capture` registration, is the model). `orgGroup` already refuses viewers.
   - It is an action path, like `POST …/screenshots/capture` and `POST …/incidents/{uid}/resolve`. It is deliberately not a `POST` on `…/run`: that resource is the multi-step run in progress (`GET`/`DELETE …/run`, `server.go:1435`), `{"running": false}` for every other check type, and a run-now request does not create it.

7. **First run on creation.** After `CreateCheck` (`server/internal/handlers/checks/service.go:1528`) inserts the jobs, send the express hint for the new check. This skips the rate limiter, since the user did not ask for the run. See *To verify*.

8. **MCP tool.** Add `run_check` (arg: check uid or slug). It calls `RunNow` and returns the response. Follow `server/internal/mcp/tools_diagnose.go`, and register the tool name in `server/internal/mcp/constants.go`.

9. **dash0.**
   - Add a `useRunCheckNow` mutation in `web/dash0/src/api/hooks.ts`, next to the capture mutation.
   - Add a "Run now" button (`Play` icon) in the check page header. It sits beside "Capture now" on capturable checks.
   - Pending state: the button shows a spinner until a result with `periodStart >= requestedAt` arrives for each queued region. Then show a toast with the new status. Time out after 2 minutes with a "still waiting" hint.
   - A 429 shows the retry delay. A 403 shows "Permission Denied" (`wiki/conventions/frontend-errors.md`).
   - Add the button pattern to the design reference if it is new.
   - Add translations for every locale dash0 ships.

10. **OpenAPI and docs.**
    - Add `/api/v1/orgs/{org}/checks/{checkUid}/run-now` (`post`, operationId `runCheckNow`) in `server/internal/app/openapi/openapi.yaml`, next to `…/screenshots/capture` (`:2292`), with a `RunNowResponse` schema.
    - Add a "Run now" section to `web/docs/docs/features/check-history.md`, or to a new `run-now.md` page.
    - Add a `CHANGELOG.md` entry following `wiki/conventions/changelog.md`.
    - List the endpoint in `wiki/api-specification/`.

## Tests

- `server/internal/handlers/checkrunnow/service_test.go` (table-driven, SQLite):
  - A 2-region check: both jobs get `scheduled_at = requestedAt`, and the response lists both as `queued`.
  - A leased job is reported `running` and its `scheduled_at` is left untouched.
  - Unknown check: `404`. A disabled check: `409`, and no window is spent.
  - A multi-step check with a run in progress (`StepRunUID` set, lease expired between slices): reported `running`, its `scheduled_at` left untouched.
  - Rate limits:
    - A second call within the minute on an http check passes (limit 3), and the fourth is refused with `RateLimitedError` and a retry delay.
    - A browser check refuses the second call.
    - The org window refuses across checks.
  - A refused org window does not burn the check window (atomic admission).
- `server/internal/handlers/checkscreenshots/*_test.go`: existing capture tests still pass unchanged after the refactor.
- `server/internal/db` (both engines): `RequestCheckRun` sets both schedule columns, leaves `capture_requested_at` NULL, and returns `sql.ErrNoRows` for a missing job.
- Route test in `server/internal/app` (like `openapi_check_runs_routes_test.go`):
  - `POST …/run-now` as a viewer returns `403`.
  - As an editor it returns `200` with `requestedAt`.
  - `GET …/run` on the same http check still returns `{"running": false}` afterwards.
- Integration (SQLite, `-short`): after `RunNow` on an http check pointed at an `httptest` server, a worker claim picks the job at once, and a result with `periodStart >= requestedAt` is stored.
- `server/internal/mcp`: `run_check` returns the response, and errors on an unknown check.
- `web/dash0/e2e/check-run-now.spec.ts`:
  - Click "Run now" on an http check, see the pending state, then see the new result.
  - A viewer does not see the button, or gets "Permission Denied".

## To verify

- Whether `CreateCheck` already makes the new job due at once and sends the express hint. If it does, drop step 7 and note it in the spec.
- The claim and release path: does a release ever overwrite a `scheduled_at` set by `RequestCheckRun` on a job that was not leased? This is the case where a worker claims between the select and the update. `RequestCheckCapture` survives this through its flag. If plain `scheduled_at` can be lost, add a guard (`WHERE lease_expires_at IS NULL OR lease_expires_at < now`) and report the job as `running`.
- The exact field the results API exposes for matching (`periodStart` or another name).

## Decisions

- **Path:** `POST …/checks/:checkUid/run-now`, an action path. Not `POST …/run`, which is the multi-step run resource.
- **Regions:** one click runs every region. With quorum, a single region cannot change the check's state.
- **Rate limits:** 3 per check per minute and 60 per org per hour for cheap types. Browser, js, rdp and vnc keep the capture limits (1 per minute, 20 per hour).
