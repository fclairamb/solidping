# Adaptive first-run polling on the check detail page

## Context

The check detail page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) polls `useResults` and `useCheck` at the check's period (`refetchInterval = parsePeriodMs(check?.period)` at line 275). For a default 60 s check, the page may not refetch for up to one minute after the backend has produced the first real result — even if the backend is fast (and even faster once the express runner spec `2026-05-03-30-express-runner-for-new-checks.md` lands).

Symptom: a user creates a check, lands on the detail page, sees the "Created" placeholder result, and sits there for up to a minute waiting for the first real status to appear.

This is purely a frontend issue. The data is already available in the backend within ~1 s of creation; the UI just doesn't ask for it often enough.

## Approach

When the latest result on the check is still the "Created" placeholder (`status === "created"` — see `server/internal/db/postgres/postgres.go:912-928`, status code 1), poll fast (every 1.5 s). Once a real status lands, fall back to the period-based interval the page already uses for steady-state.

Cap the fast-polling phase at ~30 s: if no real result appears in that window, the worker is genuinely down or the check is broken; spamming the API at 1.5 s won't help.

## Scope

**In:**
- Adaptive `refetchInterval` for `useResults` and `useCheck` on the detail page during the first-run window.
- Optional: a small "Pending first run…" pill in the header while the latest result is `created`, so the placeholder isn't mistaken for a successful first run.

**Out:**
- SSE / WebSocket push for results (separate, larger surface — would obsolete polling entirely; deferred).
- Changes to the check listing page or the public status0 dashboard (those have different cadences and a different UX bar).

## Implementation

### 1. Adaptive interval hook

In `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`:

```ts
const periodMs = useMemo(() => parsePeriodMs(check?.period), [check?.period]);

// Fast-poll while the latest result is the "Created" placeholder.
// Cap the fast phase to avoid runaway polling if the worker is down.
const isPendingFirstRun = check?.lastResult?.status === "created";
const startedAt = useRef<number>(Date.now());
const fastPollWindowMs = 30_000;
const withinFastWindow = Date.now() - startedAt.current < fastPollWindowMs;

const refetchInterval = isPendingFirstRun && withinFastWindow ? 1500 : periodMs;
```

The existing `useCheck`/`useResults` calls already consume `refetchInterval` — no plumbing changes required (lines 281-291).

`startedAt` resets per mount; the user navigating away and back resets the window, which is the right behaviour.

### 2. "Pending first run" pill

In the header section of the detail page, show a small badge / spinner when `isPendingFirstRun` is true. Reuse the existing `Badge` UI component if available, else add a plain `<span className="text-xs text-muted-foreground">Pending first run…</span>`.

### 3. Test

Playwright test in `web/dash0/tests` (the project uses Playwright per `web/dash0/CLAUDE.md`):

- Create a check via API with `period = 5m` (so the steady-state poll is slow enough to make the difference visible).
- Navigate to the detail page.
- Mock or wait for a result to appear within the fast-poll window.
- Assert the UI updates within ~2 s of the result being available, not after waiting the full 5 m period.

If a network-mocked test is too brittle, fall back to a unit test on the `refetchInterval` calculation alone.

## Verification

1. `make dev-test`.
2. Create a check with `period = 5m` against `https://example.com`.
3. Stay on the detail page with devtools Network tab open.
4. Confirm: `GET /api/v1/orgs/.../results` is requested every ~1.5 s while the latest result has `status === "created"`, then drops to once per 5 min after the first real result lands.
5. Confirm: after the 30 s cap, fast polling stops even if no result arrived (no runaway).

## Files touched

- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` — adaptive `refetchInterval` (lines ~275-291) and the optional "Pending first run" pill
- `web/dash0/tests/...` — Playwright coverage if practical

## Implementation Plan

1. Add the adaptive `refetchInterval` and ship it; verify manually as above.
2. Add the "Pending first run" pill.
3. Add Playwright coverage if the network-mock approach is reasonable; otherwise leave a unit test on the interval calculation.
