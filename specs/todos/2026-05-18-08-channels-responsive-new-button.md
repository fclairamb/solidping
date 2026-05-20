# Channels list: responsive "New channel" button

## Context

The channels list page (`/orgs/$org/channels`) has a "New channel" button that always
shows the full label, regardless of viewport width. Every other list page in the app
(on-call schedules, status pages, etc.) already follows the design-reference convention
where the label collapses to the icon alone below the `sm` breakpoint.

## Goal

Make the "New channel" button behave consistently with the rest of the app: show only
the `+` icon on small screens, show the full label on `sm` and above.

## File to change

`web/dash0/src/routes/orgs/$org/channels.index.tsx`

## Current code (approx. line 100–105)

```tsx
<Button asChild>
  <Link to="/orgs/$org/channels/new" params={{ org }}>
    <Plus className="h-4 w-4 mr-1" />
    {t("new", "New channel")}
  </Link>
</Button>
```

## Required change

Apply the canonical responsive-button pattern from the design reference:

```tsx
<Button asChild aria-label={t("new", "New channel")}>
  <Link to="/orgs/$org/channels/new" params={{ org }}>
    <Plus className="h-4 w-4" />
    <span className="hidden sm:inline">{t("new", "New channel")}</span>
  </Link>
</Button>
```

Key differences:
- Add `aria-label` on `<Button>` so screen readers announce the action when the text is hidden.
- Remove `mr-1` from the `<Plus>` icon (spacing is handled automatically when the `<span>` is visible).
- Wrap the label text in `<span className="hidden sm:inline">` so it disappears below the `sm` breakpoint.

## Acceptance criteria

- [ ] Below the `sm` breakpoint (< 640 px) the button shows only the `+` icon with no visible label.
- [ ] At `sm` and above the button shows `+ New channel` as before.
- [ ] The button still navigates to the new-channel form on click.
- [ ] No other channels-page behaviour is changed.
- [ ] `aria-label` is present on the button element.
