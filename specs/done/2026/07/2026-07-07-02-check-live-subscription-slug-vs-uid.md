# Check live subscription breaks on slug URLs — always subscribe by UID, surface WS errors in the UI

## Problem

On `https://solidping.k8xp.com/dash0/orgs/acmetech/checks/http-api-acme-io-datalake`
the realtime WebSocket subscription for the check never becomes live. The
client sends:

```json
{"type":"subscribe","entity":"check","uid":"http-api-acme-io-datalake"}
```

and the server replies:

```json
{"type":"error","code":"NOT_FOUND","title":"Check not found","entity":"check","uid":"http-api-acme-io-datalake"}
```

`http-api-acme-io-datalake` is the check's **slug**, not its UID. Two
distinct bugs stack up:

1. **The client subscribes with the raw URL param, which may be a slug.**
   The check detail route param `$checkUid` accepts either a UID or a slug —
   the REST fetch works with both because `GET /api/v1/orgs/:org/checks/:identifier`
   resolves via `GetCheckByUidOrSlug`
   (`server/internal/handlers/checks/service.go`, `GetCheck`). But the page
   passes the same raw param straight into the live subscription:

   ```tsx
   // web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:415-417
   useLiveSubscription({ entity: "check", uid: checkUid });
   useLiveSubscription({ entity: "incidents" });
   const checkLive = useScopeLive({ entity: "check", uid: checkUid });
   ```

   The WS handler validates per-check scopes with a **UID-only** lookup —
   `h.dbService.GetCheck(ctx, org.UID, scope.UID)`
   (`server/internal/handlers/realtimews/handler.go`, `handleSubscribe`) —
   which does no slug resolution (both `db/sqlite` and `db/postgres`
   `GetCheck` query by `uid` only), so a slug is rejected with `NOT_FOUND`.

2. **The error frame is silently swallowed.** The client's message switch
   explicitly ignores `error` frames
   (`web/dash0/src/lib/live-socket.ts` ~line 329: *"errors are per-message
   and non-fatal; the registry that issued the subscribe/unsubscribe doesn't
   currently need to observe them"*). `useScopeLive` never acks, so the page
   quietly stays on its polling fallback. Nothing tells the user (or a
   developer) that live updates are broken — this bug shipped invisibly.

Only one call site subscribes to a per-uid `check` scope today (the check
detail page), so the blast radius of the fix is small.

## Proposal

### A. Always subscribe with the canonical UID (client-side fix)

Subscribe with `check.uid` from the loaded check response instead of the URL
param, and gate the subscription until the check has resolved:

```tsx
// checks.$checkUid.index.tsx
const canonicalUid = check?.uid;
useLiveSubscription(
  canonicalUid ? { entity: "check", uid: canonicalUid } : undefined,
);
const checkLive = useScopeLive(
  canonicalUid ? { entity: "check", uid: canonicalUid } : undefined,
);
```

`useLiveSubscription` / `useScopeLive`
(`web/dash0/src/contexts/LiveEventsContext.tsx`) need to accept an
`undefined`/disabled scope: no-op while the check is still loading, subscribe
once the UID is known. (Hooks can't be called conditionally, so the disabled
state must be first-class.) The refcounted registry already handles the
scope-identity change (`addScope`/`removeScope` on `uidKey` change).

Why not resolve slugs server-side in `handleSubscribe` instead? Update
fan-out matches scopes **by UID** (`realtime.Scope`), so a slug-keyed
subscription would never receive events unless the server rewrote the scope
— and then the `subscribed` / `update` acks would carry the UID while the
client registry keys the scope by slug, so the ack would never match. The
client is also the party that already holds the canonical UID right after
the REST fetch. Keeping the WS protocol strictly UID-based is the simpler
contract — but it must be *enforced visibly*, hence part B.

### B. Surface WS subscription errors in the UI

Stop swallowing `error` frames end to end:

1. **`live-socket.ts`**: add an `onScopeError(scope, { code, title })`
   callback alongside `onSubscribed`; invoke it for `error` frames that carry
   a recognizable `entity` (all server error frames echo `entity` + `uid` —
   see `newError` in `server/internal/handlers/realtimews/handler.go`).
2. **`LiveEventsContext.tsx`**: record the error on the scope entry in the
   registry (cleared on successful `subscribed` ack, resubscribe, or
   reconnect), and expose it via a new `useScopeError(scope)` hook (same
   `useSyncExternalStore` pattern as `useScopeLive`).
3. **Check detail page**: when `useScopeError` reports an error, render a
   visible, non-blocking indicator that live updates are unavailable and the
   page is falling back to polling — e.g. a warning badge/tooltip next to the
   status header, using an existing primitive from the design reference
   (`web/dash0/src/routes/orgs/$org/design-reference.tsx`). Do not block the
   page: polling still works, this is a degraded-mode notice.

This covers all per-message error codes the server emits today
(`NOT_FOUND`, `VALIDATION_ERROR`, `CONCURRENCY_LIMITED` "Subscription limit
reached", `INTERNAL_ERROR`), not just the slug case — any of them currently
disappears silently.

## Files involved

- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` — subscribe
  with `check.uid`, render the degraded-mode indicator
- `web/dash0/src/contexts/LiveEventsContext.tsx` — disabled-scope support,
  per-scope error state, `useScopeError`
- `web/dash0/src/lib/live-socket.ts` — `onScopeError` callback for `error`
  frames
- `server/internal/handlers/realtimews/handler.go` — no behavior change
  expected (reference only); optionally assert in a test that error frames
  echo `entity`/`uid` so the client can key them

## Testing

- **Unit — `live-socket.test.ts`**: an `error` frame invokes `onScopeError`
  with the parsed scope and `{ code, title }`; malformed error frames stay
  ignored.
- **Unit — `LiveEventsContext.test.ts`**: disabled scope (`undefined`) never
  sends `subscribe`; scope becomes live after the UID arrives; an error frame
  sets `useScopeError` and a later `subscribed` ack clears it; reconnect
  clears stale errors.
- **E2E — `web/dash0/e2e/`**: navigate to a check via its **slug** URL and
  assert the WS subscribe frame carries the UID (or simply that the scope
  goes live / no error indicator is shown); navigate to a nonexistent check
  and assert the page-level error path still behaves.
- Backend: existing `realtimews` handler tests already cover UID validation;
  no changes expected.

## Implementation Plan

1. **`live-socket.ts` — `onScopeError` callback (Part B.1)**
   - Add `onScopeError: (scope: LiveScope, error: { code: string; title: string }) => void`
     to `LiveEventsCallbacks`.
   - In the `onmessage` switch, add a `case "error"`: if `isLiveEntity(msg.entity)`,
     call `callbacks.onScopeError({ entity: msg.entity, uid: msg.uid }, { code: msg.code, title: msg.title })`.
     Malformed/unrecognizable entity (e.g. the global "Malformed message" /
     "Unknown message type" errors which echo `entity: ""`) — silently ignored,
     matching today's `default:` behavior for anything not parseable as a scope.
   - Extend the `ServerMessage` interface with optional `code`/`title` fields.

2. **`LiveEventsContext.tsx` — disabled-scope support + per-scope error state (Part A + B.2)**
   - `ScopeEntry` gains an `error?: { code: string; title: string }` field.
   - `LiveRegistry` gains `getScopeError(scope)`, and the `onScopeError` handler:
     look up the entry by `scopeKey`, set `.error`, notify listeners. Clear
     `.error` on: successful `subscribed` ack (`onSubscribed`), reconnect
     (`onOpen` replay — clear before re-sending subscribe), and `onDisconnected`/
     `onDisabled` (stale errors from a dead connection shouldn't linger).
   - Add `useScopeError(scope: LiveScope | undefined)` hook: same
     `useSyncExternalStore` pattern as `useScopeLive`, returns
     `{ code, title } | undefined`, and returns `undefined` outright when
     `scope` is `undefined` (no subscription = no error to report).
   - Make `useLiveSubscription` and `useScopeLive` accept `LiveScope | undefined`:
     when `undefined`, the effect/store is a no-op (no `addScope`/`removeScope`
     call, `isScopeLive` always false) — this is the "gate until UID known" hook
     support the check-detail page needs. Internally represent "disabled" as a
     sentinel rather than skipping the hook call itself (hooks must run
     unconditionally every render).

3. **`checks.$checkUid.index.tsx` — canonical-UID subscription + gating (Part A)**
   - Compute `const canonicalUid = check?.uid;`.
   - Change the two `useLiveSubscription`/`useScopeLive` calls at lines 415-417
     to pass `canonicalUid ? { entity: "check", uid: canonicalUid } : undefined`
     instead of the raw `checkUid` param. Leave the `incidents` collection
     subscription (`useLiveSubscription({ entity: "incidents" })`) unchanged —
     it has no uid to resolve.
   - Add `const checkError = useScopeError(canonicalUid ? { entity: "check", uid: canonicalUid } : undefined);`.

4. **`checks.$checkUid.index.tsx` — degraded-mode UI indicator (Part B.3)**
   - When `checkError` is set, render a non-blocking warning badge/tooltip near
     the status header (reuse `Tooltip`/`TooltipTrigger`/`TooltipContent` from
     `@/components/ui/tooltip`, already used by `live-status-dot.tsx` for an
     analogous indicator) — icon `AlertTriangle` (already imported), tooltip
     text explains live updates are unavailable and the page is polling
     instead. Must not block rendering of the rest of the page (results table,
     chart, etc. keep working via polling).

5. **Tests**
   - `live-socket.test.ts`: new `describe("error frames")` — (a) an `error`
     frame with a recognizable entity invokes `onScopeError` with the parsed
     scope and `{code, title}`; (b) an error frame with an empty/unrecognizable
     entity (malformed-message case) does not invoke `onScopeError` and does
     not throw.
   - `LiveEventsContext.test.ts`: new cases — (a) `addScope`/`removeScope` are
     never called for a disabled (`undefined`) scope handed to
     `useLiveSubscription`/`useScopeLive` (registry-level: simulate via
     `LiveRegistry` directly, since the hooks require a DOM/render harness this
     suite doesn't otherwise use — verify at the registry level that no
     subscribe frame is sent when the page never calls `addScope`); (b) an
     `onScopeError` call sets `getScopeError` and notifies scope listeners;
     (c) a subsequent `onSubscribed` ack for the same scope clears the error;
     (d) `onDisconnected`/`onDisabled` clear any stale per-scope error.
   - E2E (`web/dash0/e2e/`): new spec — navigating to a check via its slug URL
     results in a `subscribe` frame carrying the UID (assert via
     `page.routeWebSocket` or `waitForEvent("websocket")` frame inspection),
     and the scope goes live / no error indicator renders. Navigating to a
     nonexistent check still hits the existing page-level not-found error path
     unaffected by this change. Author even if local `:4000` isn't in
     `SP_RUNMODE=test` — report authored-but-unrun in that case.

6. **QA gate**: `make build-dash0`, `cd web/dash0 && bun run lint` (no new
   errors in touched files), unit tests green, E2E authored (run if possible).
