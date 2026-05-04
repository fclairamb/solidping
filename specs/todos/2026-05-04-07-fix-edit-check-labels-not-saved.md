# Fix labels silently dropped when editing a check

## Context

The labels feature is shipped end-to-end:
- Backend list endpoint and label persistence work correctly.
- `LabelInput` component exists at `web/dash0/src/components/shared/label-input.tsx`.
- The shared check form emits labels in its onSubmit payload (`web/dash0/src/components/shared/check-form.tsx:745`):
  ```ts
  ...(mode === "create" || labelsDirty ? { labels } : {})
  ```

But the **edit page** (`web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx:70-80`) builds the mutation payload by hand and **doesn't pass `labels` through**:

```ts
onSubmit={async (data) => {
  await updateCheck.mutateAsync({
    name: data.name,
    slug: data.slug,
    checkGroupUid: data.checkGroupUid,
    period: data.period,
    config: data.config,
    regions: data.regions,
    reopenCooldownMultiplier: data.reopenCooldownMultiplier,
    maxAdaptiveIncrease: data.maxAdaptiveIncrease,
    // ❌ labels missing
  });
}
```

Symptom: the user changes labels in the form, hits Save, the request body has no `labels` field, the backend leaves them untouched. Labels appear unchanged on reload — silent data loss for the user's edit.

## Scope

**In scope:**
- Pass `labels` through on the edit page, with the same `labelsDirty` guard as the form's create path so we don't accidentally clear labels with a stale empty value.
- Sweep the same `onSubmit` for any **other** field the form emits but the edit page silently drops. List candidates: `description`, `internal`, `incidentThreshold`, `escalationThreshold`, `recoveryThreshold`, `enabled`. Either pass them all through or replace the manual whitelist with a typed pass-through that matches `CheckUpdateRequest`.
- Tests: a unit/integration test on the mutation hook asserting `labels` is in the request body when the user changed them; a Playwright E2E that adds a label, saves, reloads, asserts the label persists.

**Out of scope:**
- Backend changes — the `PATCH /checks/:slug` handler and service correctly persist labels when sent.
- The label autocomplete API or filter UI (already shipped).

## Approach

### 1. Fix the edit page

`web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx:70-80` — change the manual whitelist to:

```ts
onSubmit={async (data) => {
  const payload: UpdateCheckRequest = {
    name: data.name,
    slug: data.slug,
    description: data.description,
    enabled: data.enabled,
    internal: data.internal,
    checkGroupUid: data.checkGroupUid,
    period: data.period,
    config: data.config,
    regions: data.regions,
    incidentThreshold: data.incidentThreshold,
    escalationThreshold: data.escalationThreshold,
    recoveryThreshold: data.recoveryThreshold,
    reopenCooldownMultiplier: data.reopenCooldownMultiplier,
    maxAdaptiveIncrease: data.maxAdaptiveIncrease,
    ...(data.labelsDirty ? { labels: data.labels } : {}),
  };
  await updateCheck.mutateAsync(payload);
}}
```

Better yet: type the form's data shape so it *is* a `UpdateCheckRequest` (or a superset that maps trivially) and stop hand-rolling the whitelist. If that's a larger refactor, leave a TODO and just fix the omission for this release.

(`labelsDirty` needs to be passed up from the form. If it isn't, expose it via `useFormContext` or as a callback arg to `onSubmit`.)

### 2. Tests

**Unit / hook test** (`web/dash0/src/api/hooks.test.ts` or similar):
- Build a fake fetch, call `updateCheck.mutate({labels: [{key: "env", value: "staging"}]})`, assert the captured request body includes labels.

**Playwright E2E** (`web/dash0/e2e/check-labels.spec.ts`):
1. Log in as test user.
2. Create or open a check.
3. Add a label `env=staging` via the LabelInput.
4. Click Save.
5. Reload the edit page.
6. Assert the label chip is present.
7. Remove the label, save, reload, assert it's gone.

### 3. Audit other dropped fields

Whatever the sweep reveals, fix them in this same PR. Each silently-dropped field is the same class of bug.

## Verification

1. `make test-dash` passes the new tests.
2. `make dev`, log in, open any check, add a label, save, reload — label persists. Repeat for description, internal toggle, threshold fields.
3. Inspect the network request on Save: every visible form field is in the payload.
