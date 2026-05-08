# Pre-fill channel name from the integration label

## Context

Channel creation lives at
`web/dash0/src/routes/orgs/$org/channels.new.tsx`. Flow:

1. User clicks "+ New channel" → lands on a type picker (Slack,
   Discord, Webhook, Email, Google Chat, Mattermost, ntfy, Opsgenie,
   Pushover).
2. User picks a type → flips into the form (`ChannelForm` in
   `web/dash0/src/components/channels/channel-form.tsx`).
3. The form's `name` field starts empty (`useState(initial?.name || "")`
   at L28), is `required`, and shows the placeholder
   `"On-call alerts"` (form.namePlaceholder).

The friction: the user *just* picked "Slack", and now the form asks
them to invent a name before they can submit. For most orgs the
first Slack channel is just "Slack" — the placeholder isn't a default,
so the user has to type something. This blocks submit and adds a
pointless step in the most-used activation path.

The DB has no uniqueness constraint on `integration_connections.name`
(see `001_initial.up.sql` — only org+type+slug-style indexes, no
unique on name). So multiple channels named "Slack" are legal; users
can rename later.

## Honest opinion

1. **Default the name to `channelLabel(type)` when type is picked.**
   `channelLabel` already produces "Slack", "Discord", "Webhook",
   "Email", "Google Chat", "Mattermost", "ntfy", "Opsgenie",
   "Pushover" (see
   `web/dash0/src/components/channels/channel-icon.tsx` L34–57).
   Reuse it — don't introduce a new map.
2. **Do it on `new`, not on `edit`.** The detail page
   (`channels.$connectionUid.tsx`) loads the saved name; pre-filling
   would clobber a user-chosen name. The default belongs *only* in
   the create flow, gated on "user just picked a type for a brand-new
   channel".
3. **Treat the default like a placeholder you can edit.** Pre-fill,
   but don't lock. If the user changes it once, do not stomp on
   their value if they switch type and switch back. (Implementation
   note: only auto-fill when name is empty AND has never been
   touched, OR when name still equals the previous type's label.)
4. **Don't extend this to on-call / escalation policies.** They have
   no "integration" — a default like `"On-call schedule"` /
   `"Escalation policy"` is just a placeholder dressed up. The bigger
   UX papercut on those forms is the redundant slug+name pair (slug
   should auto-derive from name, like checks already do). That's a
   different spec; out of scope here.
5. **Dependencies have no name field.** Pure edges. N/A.
6. **Slug for channels is not user-facing** — channels are UID-routed,
   so we don't need to default a slug field. (Verified by reading
   `channels.new.tsx` — there's no slug input.)

## Scope

In scope:

- `web/dash0/src/routes/orgs/$org/channels.new.tsx`: when the user
  picks a type from the picker, propagate the type label as the
  initial name into `ChannelForm`.

- `web/dash0/src/components/channels/channel-form.tsx`: support an
  `initialName` prop (or wire it through `initial`) that seeds the
  `name` state. Behavior:
  - If `initial?.name` is set (edit mode), use it. Unchanged.
  - Else if `initialName` is provided (new mode, type just picked),
    use it.
  - Else empty.

Out of scope:

- Default names for on-call / escalation policies (see "Honest
  opinion" #4).
- Slug auto-derivation on on-call / escalation new pages (different
  paper cut; track separately).
- Dependencies (no name field).
- Any uniqueness constraint or rename-suffix logic. Two channels
  named "Slack" is fine.

## Implementation

### `channel-form.tsx`

Add an `initialName` prop:

```tsx
interface ChannelFormProps {
  type: ConnectionType;
  initial?: Connection | null;
  initialName?: string;     // NEW
  onChange: (state: ChannelFormState) => void;
}

export function ChannelForm({ type, initial, initialName, onChange }: ChannelFormProps) {
  const [name, setName] = useState(initial?.name || initialName || "");
  // … rest unchanged
}
```

That `useState` initializer fires once per mount, so the seed only
applies on first render. If the parent unmounts and remounts the
form (e.g., the user clicks "Change type"), the new `initialName`
will seed the new mount — exactly the desired behavior.

### `channels.new.tsx`

Where the form is rendered (~L132–134), pass the type label:

```tsx
<ChannelForm
  type={type}
  initialName={channelLabel(type)}
  onChange={setForm}
/>
```

`channelLabel` is already imported alongside `ChannelIcon` at L18.

That's it. The "Change type" button at L137–143 sets `type` back to
`null`, which unmounts `ChannelForm` (the parent renders the type
picker again instead). Re-picking a type re-mounts the form with the
new label as the seed.

### Edge: user typed a name, then changed type

This is the reason `useState` initializer is the right choice:

- Pick "Slack" → name seeds to `"Slack"`.
- User types `"Production alerts"` → state becomes
  `"Production alerts"`.
- User clicks "Change type", picks "Discord" → form unmounts and
  remounts with `initialName="Discord"` → name seeds to `"Discord"`.

This is acceptable. The user explicitly threw away the form by
changing type; we re-seed from scratch. If we tried to "preserve" the
user's typed name across type changes, we'd need to track it in the
parent — extra state for a vanishing edge case. Don't.

## Files to modify

- `web/dash0/src/components/channels/channel-form.tsx` — add
  `initialName` prop, seed `useState`.
- `web/dash0/src/routes/orgs/$org/channels.new.tsx` — pass
  `initialName={channelLabel(type)}` to `<ChannelForm>`.

No translation file edits (the placeholder text in `channels.json`
`form.namePlaceholder` stays — it just becomes redundant for the
new-channel path; keep it for safety in case `initialName` is
omitted somewhere).

No new hooks. No backend changes. No DB changes.

## Verification

Manual:

1. Click "+ New channel" → pick "Slack" → form opens with name field
   pre-filled to "Slack". Submit works without editing.
2. Pick "Discord" → name pre-filled to "Discord". Same for each of
   the 9 types.
3. Type "Production alerts" over the default → it sticks. Submit
   succeeds with "Production alerts".
4. Click "Change type" → pick "Webhook" → name re-seeds to "Webhook"
   (the typed value is discarded, by design).
5. Open an existing channel's detail page → name still loads from
   the saved channel (no clobbering).

Playwright in `web/dash0/e2e/channels.spec.ts` (or a new one):

- Navigate to `/orgs/$org/channels/new?type=slack` → assert the name
  input value is `"Slack"`.
- Same for `?type=discord`, `?type=webhook`, `?type=email`.
- Submit immediately (no typing) → assert successful redirect to the
  detail page with name "Slack".

## Critical files

- `web/dash0/src/routes/orgs/$org/channels.new.tsx` L131–134 — where
  `<ChannelForm>` is rendered.
- `web/dash0/src/components/channels/channel-form.tsx` L26–37 — name
  state init.
- `web/dash0/src/components/channels/channel-icon.tsx` L34–57 —
  `channelLabel`. Reuse, don't duplicate.
