# Move Cancel button to the top right on the new-channel page

## Context

The `/dash0/orgs/$org/channels/new` page has two visual states:

1. **Type picker** — a grid of channel-type cards with a Cancel button sitting
   below the grid at the bottom-left (inside a bare `<div>`).
2. **Form** (`type` selected) — a `Card` with channel fields and a footer row
   containing "Change type" + "Create channel". There is no Cancel button at
   all to abort back to the channel list.

Both states bury or omit the escape hatch. The convention across the app is a
top-right action in the page header (see `checks.$checkUid.edit.tsx`,
`escalation-policies.new.tsx`).

## Goal

Place a **Cancel** button (ghost, links to `/orgs/$org/channels`) in the
top-right of the page header for both states of the new-channel flow, so users
can always abandon without scrolling to the bottom of the page.

## Scope

File: `web/dash0/src/routes/orgs/$org/channels.new.tsx`

### Type-picker state

Replace the bottom `<div>` that contains the Cancel button with a
page-header row that has the title/subtitle on the left and the Cancel
button aligned to the right:

```tsx
<div className="flex items-start justify-between gap-4">
  <div>
    <h1 className="text-2xl font-bold tracking-tight">
      {t("newTitle", "Add a notification channel")}
    </h1>
    <p className="text-sm text-muted-foreground">
      {t("newSubtitle", "Pick the channel type to configure.")}
    </p>
  </div>
  <Button asChild variant="ghost" size="sm">
    <Link to="/orgs/$org/channels" params={{ org }}>
      {t("cancel", "Cancel")}
    </Link>
  </Button>
</div>
```

Remove the standalone `<div>` with the Cancel button that currently sits
below the type grid.

### Form state (Card)

Move the Cancel link into `CardHeader` alongside the title, using the same
right-aligned pattern:

```tsx
<CardHeader>
  <div className="flex items-center justify-between gap-4">
    <CardTitle className="flex items-center gap-2">
      <ChannelIcon type={type} className="h-5 w-5" />
      {channelLabel(type)}
    </CardTitle>
    <Button asChild variant="ghost" size="sm">
      <Link to="/orgs/$org/channels" params={{ org }}>
        {t("cancel", "Cancel")}
      </Link>
    </Button>
  </div>
  <CardDescription>
    {t("newFormSubtitle", "Configure the channel and save.")}
  </CardDescription>
</CardHeader>
```

Keep the "Change type" button in the form footer (it's a different action —
going back to the picker, not aborting the flow).

## Acceptance criteria

- [ ] In the type-picker state, a Cancel button appears top-right of the
      page header and navigates to the channels list.
- [ ] The bottom-only Cancel button is gone from the type-picker state.
- [ ] In the form state, a Cancel button appears top-right inside the card
      header and navigates to the channels list.
- [ ] "Change type" remains in the form footer row unchanged.
- [ ] No visual regression on the form footer ("Change type" + "Create
      channel" still right-aligned at the bottom).
