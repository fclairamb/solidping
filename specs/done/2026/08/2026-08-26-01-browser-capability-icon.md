---
model: sonnet
effort: medium
---

# Regions advertise a browser capability, but the dashboard never shows it

## Problem

Since spec 2026-08-19-03, workers self-report a `browser` capability (whether
they have a Chrome to drive) and the regions API aggregates it per region with
the same three-state `yes` / `no` / `unknown` semantics as IPv6
(`server/internal/regions/capabilities.go:26-31` — `CapabilityBrowser`). The
value is already on the wire in the public regions response, exactly like
`ipv4` / `ipv6`.

But dash0 renders nothing for it. A user creating a `browser` check picks
regions blind: nothing tells them which regions can actually run a browser
check, while IPv6 gets a full three-state badge
(`web/dash0/src/components/shared/ipv6-capability.tsx`) in the check form's
region picker (`web/dash0/src/components/shared/check-form.tsx`).

The IPv6 badge is a text badge ("IPv6" / "No IPv6" / "IPv6 unknown"). Adding a
second text badge per region would crowd the picker — the browser capability
should render as a **single icon** instead.

## Proposal

Add a `BrowserCapabilityIcon` component alongside the IPv6 badge, surfacing the
region's `browser` capability as one icon (e.g. lucide `Chrome` or `AppWindow`)
with a tooltip, mirroring the IPv6 component's contract:

- **Same three states, never collapsed.** `unknown` is a real answer (older
  agent, no live worker) and must not render as "no" — reuse the exact
  semantics and copy style of `ipv6-capability.tsx`, including a
  `hideUnknown` prop for inline surfaces.
- **Icon-only rendering.** One icon whose color/variant encodes the state
  (success / warning / neutral outline), with the explanation in the tooltip —
  no label text. Keep a `data-browser={capability}` attribute for tests.
- **UI hint only, never a gate.** Like IPv6, the advertised value must not
  hide, filter or disable a region — the runtime worker remains the authority.
- **Surfaces:** the region picker in `check-form.tsx` (at minimum when the
  check type is `browser`; showing it for all types is acceptable if it stays
  quiet via `hideUnknown`), and the private locations page
  (`web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx`)
  next to the existing IPv6 badge.
- **Design reference:** add the new component to
  `web/dash0/src/routes/orgs/$org/design-reference.tsx` per the repo's frontend
  convention.
- Add a small helper `browserCapability(capabilities)` mirroring
  `ipv6Capability()` (absent map → `unknown`), plus unit coverage mirroring the
  IPv6 component's tests, and locale keys for the tooltip strings in all four
  locales if the surrounding surface is localized.

### Open questions

- Whether the icon should also appear in region sorting (the check form sorts
  regions by IPv6 capability at `check-form.tsx:606`) when the check type is
  `browser` — sorting browser-capable regions first would be a natural
  extension, but is not required by this spec.

### Non-goals

- No backend changes: reporting, aggregation and the wire format already exist.
- No gating/filtering of regions based on the capability.
