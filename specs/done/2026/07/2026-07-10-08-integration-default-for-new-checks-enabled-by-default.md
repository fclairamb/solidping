# The "Default for new checks" toggle should start enabled when creating an integration

## Problem

When creating a new integration of any type, the **Default for new checks**
toggle ("Pre-checked when creating a new check. Existing checks are
unaffected.") starts **off**. Most users who set up a notification channel
want it wired into new checks by default, so the current default forces an
extra deliberate toggle on almost every integration and makes it easy to
create a channel that silently never attaches to any check.

The default is set in the shared integration form:

```ts
// web/dash0/src/components/integrations/integration-form.tsx:83
const [isDefault, setIsDefault] = useState(initial?.isDefault ?? false);
```

For a new integration `initial` is `null`, so the toggle lands on `false`.

## Proposal

Flip the create-flow default so the toggle starts **on** for all integration
types.

- In [integration-form.tsx:83](web/dash0/src/components/integrations/integration-form.tsx:83),
  change `initial?.isDefault ?? false` to `initial?.isDefault ?? true`.
  Because editing passes a real `initial`, the stored value is still
  respected on the edit flow — only the create flow (where `initial` is
  `null`) changes. The form is shared across every integration type, so this
  one change covers "all integrations".

### Open questions / to confirm

- **Backend/API/MCP parity.** The DB model and API default `isDefault` to
  `false` when the field is omitted
  ([integration.go:73](server/internal/db/models/integration.go:73),
  [integration.go:98](server/internal/db/models/integration.go:98); MCP create
  at [tools_integrations.go:72](server/internal/mcp/tools_integrations.go:72)).
  Decide whether "enabled by default" is a UI-only affordance (the form always
  sends an explicit `isDefault`, so the UI change alone is sufficient) or a
  product-wide default that should also flip on the create paths that omit the
  field. Leaning UI-only, since the form always submits the toggle's value —
  but confirm the API/MCP create default is acceptable as-is.
- **Tests.** Update any e2e expectation that asserts the toggle is off on the
  create screen — see `web/dash0/e2e/integrations.spec.ts` and
  `web/dash0/e2e/channels-slack-install.spec.ts`.

## Implementation Plan

Scope: **UI-only**, matching the spec's leaning. The shared integration form
always submits an explicit `isDefault` value with every create/edit request, so
flipping the form's create-flow default is sufficient — no backend/API/MCP
change is needed, and the DB/API/MCP defaults of `false` (used only when the
field is omitted, which the form never does) are left untouched.

1. **Flip the create-flow default** in
   `web/dash0/src/components/integrations/integration-form.tsx:83`: change
   `useState(initial?.isDefault ?? false)` to
   `useState(initial?.isDefault ?? true)`. Edit flows pass a real `initial`, so
   the stored value is still respected; only the create flow (`initial === null`)
   changes. The form is shared across every integration type, so this covers all
   integrations.

2. **E2E coverage** in `web/dash0/e2e/integrations.spec.ts`: in the webhook
   create flow, after picking the integration type and before submitting, assert
   the "Default for new checks" toggle (`#ch-default`) is checked. This locks in
   the new create-flow default.

3. **Verify existing tests are unaffected.** The `isDefault: false` occurrences
   in `web/dash0/e2e/integrations.spec.ts` (lines 62, 114) and
   `web/dash0/e2e/channels-slack-install.spec.ts` (line 91) are all edit-flow
   mock responses (existing integrations with a real `initial`), not create-flow
   assertions, so they need no change.

4. **QA.** `make build-dash0` and `cd web/dash0 && bun run lint` (no new errors
   in touched files). Run/author the affected E2E file.
