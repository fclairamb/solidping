# Mobile: Collapse "New channel" button to "+" icon on small screens

## Problem

On mobile viewports, the "New channel" button on `/orgs/:org/channels` takes up
significant horizontal space in the page header. On narrow screens it should
reduce to a compact icon-only `+` button to preserve header real estate.

## Expected behaviour

| Viewport | Appearance |
|---|---|
| ≥ `sm` (640 px) | Full `+ New channel` button (current) |
| < `sm` | Icon-only `+` button, same action, same visual style |

The icon-only variant must:
- Keep the same primary colour and border-radius as the full button.
- Remain accessible: include `aria-label="New channel"` so screen readers and
  automated tests can target it.
- Navigate to the same "new channel" route as the full button.

## Implementation notes

- Locate the "New channel" button in the channels list page (likely
  `web/dash0/src/routes/orgs/$org/channels/`).
- Use Tailwind responsive utilities to toggle between the two variants:
  ```tsx
  {/* Mobile: icon only */}
  <Button className="sm:hidden" aria-label="New channel"><Plus /></Button>

  {/* Desktop: full label */}
  <Button className="hidden sm:flex"><Plus /> New channel</Button>
  ```
- Reuse the existing `Button` and `Plus` (lucide-react) primitives — no new
  components needed.
- Check the design reference at `/dash0/orgs/default/design-reference` for the
  exact button variant in use.

## Acceptance criteria

- [ ] On a viewport < 640 px wide the button shows only the `+` icon (no text).
- [ ] On a viewport ≥ 640 px the full "New channel" label is visible.
- [ ] Clicking/tapping either variant navigates to the new-channel route.
- [ ] `aria-label="New channel"` is present on the icon-only button.
- [ ] No layout overflow or wrapping occurs in the page header on 320 px width.
