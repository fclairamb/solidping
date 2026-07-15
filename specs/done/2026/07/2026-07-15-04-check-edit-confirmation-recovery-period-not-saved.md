---
model: sonnet
effort: medium
---

# Editing a check's confirmation or recovery period silently fails to save

## Problem

On the check edit page, changing the "confirmation period" or "recovery period"
values and saving does not persist the change — the check keeps its old
`confirmationPeriodSeconds` / `recoveryPeriodSeconds` values.

Root cause: [web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx:107-121](web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx#L107-L121)
builds the `updateCheck` PATCH payload **by hand**, field by field, instead of
forwarding the full `data` object the form produces:

```tsx
onSubmit={async (data) => {
  await updateCheck.mutateAsync({
    enabled: data.enabled, name: data.name, slug: data.slug,
    checkGroupUid: data.checkGroupUid, period: data.period, config: data.config,
    regions: data.regions, reopenCooldownMultiplier: data.reopenCooldownMultiplier,
    flappingWindowSeconds: data.flappingWindowSeconds, flapBackoffFactor: data.flapBackoffFactor,
    maxRecoveryMultiplier: data.maxRecoveryMultiplier,
    ...(data.labels !== undefined ? { labels: data.labels } : {}),
  });
```

`data.confirmationPeriodSeconds` and `data.recoveryPeriodSeconds` are simply
missing from this list, so they're silently dropped before the request is even
sent.

Everything else in the chain is confirmed correct:
- The form itself is wired correctly end to end — state seeded from
  `initialData`, inputs bound with `value`/`onChange`
  ([web/dash0/src/components/check-form.tsx:375-380](web/dash0/src/components/check-form.tsx#L375-L380),
  [:1000](web/dash0/src/components/check-form.tsx#L1000),
  [:1015](web/dash0/src/components/check-form.tsx#L1015)), and the form's own
  `onSubmit` builder does include both fields in the `data` it hands to the
  caller ([check-form.tsx:554-559](web/dash0/src/components/check-form.tsx#L554-L559)).
- `UpdateCheckRequest` ([web/dash0/src/api/hooks.ts:110-127](web/dash0/src/api/hooks.ts#L110-L127))
  declares both fields, and `useUpdateCheck` does a plain `JSON.stringify` with
  no stripping ([hooks.ts:337-352](web/dash0/src/api/hooks.ts#L337-L352)).
- The backend is fully correct: `UpdateCheckRequest` struct, validation, and
  `models.CheckUpdate` application in
  [server/internal/handlers/checks/service.go:1182-1209,1364-1375](server/internal/handlers/checks/service.go#L1182-L1209),
  and both DB backends include the columns in their `UPDATE` SET clause
  ([server/internal/db/postgres/postgres.go:1455-1459](server/internal/db/postgres/postgres.go#L1455-L1459),
  [server/internal/db/sqlite/sqlite.go:1408-1412](server/internal/db/sqlite/sqlite.go#L1408-L1412)).

So this is purely a stale manual field list in the edit route's submit handler
— most likely these two fields (added in spec `2026-05-08-02`) were never added
to this list, while the later flapping fields (spec `2026-06-30-07`) were,
confirming it's an omission rather than an intentional restriction.

Note: `checks.new.tsx` (the *create* route, lines 160-171) also omits these
fields when building its payload, but `CreateCheckRequest` in `hooks.ts`
doesn't declare them at all — setting confirmation/recovery period at
check-creation time looks like a separate, pre-existing gap. Out of scope for
this spec, which is about the reported *edit* bug.

## Proposal

1. Fix [checks.$checkUid.edit.tsx](web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx)'s
   `onSubmit` handler to include `confirmationPeriodSeconds` and
   `recoveryPeriodSeconds` in the `updateCheck.mutateAsync(...)` payload,
   alongside the other fields already forwarded there.
2. Prefer forwarding the whole `data` object (spread) over continuing to hand-list
   every field, if that's a safe/contained change — it eliminates this entire
   class of bug (a field added to the form later getting silently dropped here
   again). If a full spread isn't safe (e.g. because `data` carries extra
   client-only fields the API type doesn't expect), keep the explicit list but
   add the two missing fields, and leave a short comment noting the list must
   stay in sync with the form's fields.
3. Add an end-to-end regression test under `web/dash0/e2e/` (there's no
   dedicated "check edit" spec yet — follow the pattern of
   `web/dash0/e2e/checks.spec.ts` / `check-form-progressive-disclosure.spec.ts`,
   which already reference the `confirmation-period-input` /
   `recovery-period-input` test ids). The test should: open an existing
   check's edit page, change the confirmation period and recovery period
   values, save, reload/re-navigate to the edit page, and assert the new
   values are shown (i.e. actually persisted server-side, not just held in
   client state).

## Implementation Plan

1. **Fix the submit handler** in
   [checks.$checkUid.edit.tsx](web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx):
   add `confirmationPeriodSeconds: data.confirmationPeriodSeconds` and
   `recoveryPeriodSeconds: data.recoveryPeriodSeconds` to the object passed to
   `updateCheck.mutateAsync(...)`.
   - Decision: keep the explicit hand-listed object rather than spreading the
     full `data` object. `CheckFormData` (check-form.tsx) is a superset of
     `UpdateCheckRequest` (hooks.ts) — it also carries `type`,
     `connectionUids`, `dependsOnParentUids`, and
     `initialDependsOnParentUids`, which are not PATCH-check fields and are
     applied separately in this same handler via `setConnections`,
     `createDep`/`deleteDep`. Spreading `data` directly into the PATCH body
     would ship those extra client-only fields to the server. So: keep the
     explicit list (matching the Proposal's documented fallback) and add a
     comment at the call site noting the list must be kept in sync with the
     form's `data` shape, so a future field addition fails loudly (via a
     missing-field bug report) rather than silently.
2. **Run `make fmt`** to keep formatting consistent.
3. **Add an e2e regression test** — new file
   `web/dash0/e2e/check-edit-period-persistence.spec.ts`, following the
   login/setup pattern of `web/dash0/e2e/checks.spec.ts`: create (or reuse) a
   check, open its edit route, change
   `[data-testid=confirmation-period-input]` and
   `[data-testid=recovery-period-input]`, save, re-navigate to the edit route
   (or reload), and assert both inputs show the newly saved values — proving
   server-side persistence, not just client state.
4. **QA**: `make build-dash0`, `cd web/dash0 && bun run lint` (no new errors
   in touched files), then run the new e2e spec against a `SP_RUNMODE=test`
   server if one can be stood up without disturbing the `:4000` devloop.
