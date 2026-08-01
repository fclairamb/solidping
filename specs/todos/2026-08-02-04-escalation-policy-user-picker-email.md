---
model: sonnet
effort: low
---

# Escalation policy user picker shows only the name, making same-named users indistinguishable

## Problem

On `/dash0/orgs/$org/escalation-policies/new` (and the edit page, which shares
the same component), the **User** target dropdown renders each org member as
`{m.name || m.email}` —
[step-target-row.tsx:166](web/dash0/src/components/escalation/step-target-row.tsx).
The email is only shown when the name is empty.

When two members have the same display name (e.g. two "Florent Clairambault"
entries), the dropdown shows two identical rows and there is no way to tell
which user is being selected. The selected value shown in the closed trigger
has the same ambiguity.

The on-call schedule form already solved this exact problem: it renders the
name followed by the email in muted text —
[on-call-schedule-form.tsx:271-272](web/dash0/src/components/oncall/on-call-schedule-form.tsx):

```tsx
{m.name || m.email}{" "}
<span className="text-muted-foreground">({m.email})</span>
```

## Proposal

In `UserSelect` inside
[step-target-row.tsx](web/dash0/src/components/escalation/step-target-row.tsx)
(~line 164-168), mirror the on-call schedule form's pattern:

- Render each `SelectItem` as `{m.name || m.email}` followed by
  `<span className="text-muted-foreground">({m.email})</span>` — but only
  append the `(email)` suffix when `m.name` is non-empty, to avoid showing the
  email twice for name-less members (the on-call form shows
  `email (email)` in that case; don't replicate that wart).
- The closed `SelectValue` inherits the item content in this Select
  implementation, so the trigger will show the email too — that is desired
  (the screenshot ambiguity applies to the trigger as well). Verify it
  truncates gracefully in the `h-8 flex-1` trigger on narrow/mobile widths
  (`truncate` on the item text if needed).
- Keep the existing sort (`(a.name || a.email).localeCompare(...)` at
  [step-target-row.tsx:146](web/dash0/src/components/escalation/step-target-row.tsx));
  optionally add `.email` as a tie-breaker so same-named users order
  deterministically.

Both the new and edit escalation policy routes go through `StepTargetRow`, so
one change covers both.

Optionally, apply the same "skip the suffix when the name is empty" guard to
[on-call-schedule-form.tsx:271-272](web/dash0/src/components/oncall/on-call-schedule-form.tsx)
for consistency.

Add/extend a Playwright E2E (or component-level assertion in an existing
escalation policy E2E) that a member's email is visible in the user target
dropdown options.
