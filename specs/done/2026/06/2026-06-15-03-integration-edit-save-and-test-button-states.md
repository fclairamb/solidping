# Integration edit page: gate Save on changes and Send test on a saved state

## Context
The integration edit page lets you change a notification integration and fire a
test delivery, but two buttons behave loosely:

- **Save changes**
  (`web/dash0/src/routes/orgs/$org/integrations.$integrationUid.tsx:246`) is
  disabled only on `update.isPending || !form?.name`. With a valid name it stays
  active even when nothing changed, so a user can "save" a no-op.
- **Send test** lives in `TestNotificationSection`
  (`web/dash0/src/components/integrations/integration-form.tsx:178`) and is
  disabled only on `test.isPending`. The test always uses the **persisted**
  settings (`integration-form.tsx:138-140`), so running it with unsaved edits
  silently tests the old config. This is only hinted at in the help copy
  (`integration-form.tsx:174`).

There is an established in-repo pattern for change-tracking: `server.auth.tsx`
and `server.slack.tsx` compute an `isDirty` flag by comparing current state to
the persisted values and disable Save when not dirty.

## Goal
Derive both buttons from a single "are there unsaved changes?" (`isDirty`)
signal so they are exact complements:

- **Clean** (no unsaved edits): Save is greyed/disabled; Send test is enabled.
- **Dirty** (unsaved edits): Save is enabled; Send test is disabled and shows a
  hover tooltip explaining it needs the integration saved first.

On page load of an already-saved integration the form is clean, so Send test is
enabled immediately — it is only disabled once the user edits something.

## Behaviour

### 1. Compute `isDirty` on the edit page
In `integrations.$integrationUid.tsx` (`IntegrationDetailPage`), compare the
current `form` state (already collected via `IntegrationForm`'s `onChange`) to
the loaded `integration`:

```ts
const isDirty =
  form != null &&
  (form.name !== integration.name ||
    form.enabled !== integration.enabled ||
    form.isDefault !== integration.isDefault ||
    JSON.stringify(form.settings) !==
      JSON.stringify(integration.settings ?? {}));
```

`form` is `null` only for the first render before `IntegrationForm`'s mount
effect fires `onChange`; treat `null` as **not** dirty (Save disabled, Send test
enabled). `settings` is compared with `JSON.stringify`: edits mutate it as
`{ ...settings, [key]: value }`, which preserves existing key order, so the
serialized comparison is stable for this form. (If a future panel reorders keys,
swap in a small stable deep-equal — but it is not needed today.)

### 2. Save changes — disable when clean
Add `!isDirty` to the existing disabled condition:

```tsx
disabled={update.isPending || !form?.name || !isDirty}
```

The Button's base `disabled:opacity-50` already renders it greyed — no variant
change needed (the request is "grey/disabled", matching the default).

### 3. Send test — disable when dirty, with a tooltip
Thread the saved/clean signal down: pass a prop (e.g. `canTest={!isDirty}`) from
the page into `<IntegrationForm>`, and from `IntegrationForm` into
`<TestNotificationSection>`. `IntegrationForm` already renders
`TestNotificationSection` at `integration-form.tsx:126-128`.

In `TestNotificationSection`:
- Disable when there are unsaved edits or a test is in flight:
  `disabled={!canTest || test.isPending}`.
- Wrap the button in a tooltip that warns only when it is disabled due to
  unsaved edits. A natively-`disabled` button does not emit hover events
  (`disabled:pointer-events-none` plus the native `disabled` attribute), so the
  tooltip trigger must be a focusable wrapper around the button, not the button
  itself:

```tsx
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";

<Tooltip>
  <TooltipTrigger asChild>
    <span tabIndex={0} className="inline-flex">
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={!canTest || test.isPending}
        onClick={handleTest}
        data-testid="webhook-send-test"
      >
        {test.isPending ? (
          <Loader2 className="mr-1 h-4 w-4 animate-spin" />
        ) : (
          <Send className="mr-1 h-4 w-4" />
        )}
        {t("form.sendTest", "Send test")}
      </Button>
    </span>
  </TooltipTrigger>
  {!canTest && (
    <TooltipContent data-testid="webhook-send-test-tooltip">
      {t(
        "form.testNeedsSave",
        "Save your changes first — the test uses the saved settings.",
      )}
    </TooltipContent>
  )}
</Tooltip>
```

`TooltipProvider` is already mounted globally in `main.tsx`, so no provider is
needed here.

### 4. Tidy the help copy (optional)
With the disabled state + tooltip now carrying the "save first" message, soften
the static help text at `integration-form.tsx:171-176` so it isn't redundant —
e.g. keep only "Deliver a sample alert to confirm this integration is wired up.
The test uses the saved settings." Keep the `form.testHelp` key.

## Out of scope
- The create/new integration flow (separate route) — this is the edit page only.
- Any change to what the test actually sends, or to the save/update API call.
- Navigation-blocking / "unsaved changes" prompts on leaving the page.
- Webhook signing-secret Copy/Rotate buttons and the Slack picker — untouched.
- The Freebox panel, which has no test button (`integration-form.tsx:126`).

## Testing
dash0 Playwright E2E lives in `web/dash0/e2e/`; integration coverage is in
`web/dash0/e2e/integrations.spec.ts` — extend it:
- On opening a saved integration: Save is disabled, Send test
  (`webhook-send-test`) is **enabled**.
- After editing a field (e.g. the name): Save becomes enabled and Send test
  becomes disabled.
- Hovering the disabled Send test shows the tooltip
  (`webhook-send-test-tooltip`); the tooltip is absent when the button is
  enabled.
- Saving returns to the clean state: Save disabled, Send test enabled again.

The Save button currently has no `data-testid` (it uses `aria-label`); add a
`data-testid` (e.g. `integration-save`) if the test needs a stable handle.

Manual: `make dev-test`, open an existing integration at
`/dash0/orgs/<org>/integrations/<uid>`, and verify the table above in desktop +
mobile, light + dark. Confirm `bun run lint` and `make test-dash` pass.

## Implementation Plan
1. In `integrations.$integrationUid.tsx`, compute `isDirty` (section 1) and add
   `!isDirty` to the Save button's `disabled` (section 2). Pass `canTest={!isDirty}`
   into `<IntegrationForm>`.
2. In `integration-form.tsx`, add a `canTest` prop to `IntegrationForm` and
   forward it to `TestNotificationSection`; add a `canTest` prop there and apply
   the disabled state + tooltip wrapper (section 3). Optionally tidy the help
   copy (section 4).
3. Extend `web/dash0/e2e/integrations.spec.ts` with the cases above; add an
   `integration-save` test-id if needed.
4. Verify: `bun run lint` (dash0), `make test-dash`, manual check.
