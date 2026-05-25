# Discovery: unified "Start new scan" with scan-method selector (frontend)

## Context

The discovery page currently has **two unrelated entry points** for starting a
scan:

1. **"Start new scan"** link → navigates to `/orgs/$org/discovery/new`
   (`discovery.new.tsx`) — a CIDR-based LAN scan form only.
2. **"Discover via Freebox"** dropdown (`FreeboxLauncher`, lines 98-136 of
   `discovery.index.tsx`) — lists paired Freebox channels; selecting one fires
   `useStartFreeboxScan` immediately with no form, no confirmation.

The split means: the user must know about the second entry point to use the
Freebox method; the Freebox path gets no confirmation step; and since spec
`2026-05-25-08` will make the Freebox scan an active probe (ICMP + port scan),
launching it without a confirmation checkpoint is inconsistent with the LAN scan.

There is also a latent bug: `FreeboxLauncher` filters channels by `type ===
"freebox"` only, while `check-form.tsx` (line 1274) correctly additionally
requires `settings.status === "granted"`. A non-granted channel appears in the
dropdown but will fail at the backend.

## Goal

One unified "Start new scan" flow:

- **`/orgs/$org/discovery/new`** gets a **"Scan method"** `Select` at the top
  (default LAN).
- **LAN** selected → existing CIDR fields + advanced options + confirmation.
- **Freebox** selected → a channel `Select` listing only granted Freebox channels,
  plus the same confirmation checkbox.
- Submit dispatches to the right API hook and navigates to the resulting scan
  detail page.
- The separate **"Discover via Freebox" dropdown is removed** from the index
  page; the single "Start new scan" link remains.

## Non-goals

- New backend endpoints — `useStartDiscoveryScan` and `useStartFreeboxScan`
  already exist in `web/dash0/src/api/hooks.ts`.
- Changes to the scan detail or host promotion pages.
- Touching
  [`components/shared/freebox-lan-discovery.tsx`](../../web/dash0/src/components/shared/freebox-lan-discovery.tsx)
  — that is the per-check live host picker (unrelated).
- Backend changes (covered by spec `2026-05-25-08`).

## `discovery.new.tsx` changes

File:
[`web/dash0/src/routes/orgs/$org/discovery.new.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.new.tsx)

### Scan-method select

Add at the top of the form, above any other fields:

```tsx
<Select value={method} onValueChange={setMethod}>
  <SelectTrigger>
    <SelectValue placeholder={t("scanMethod")} />
  </SelectTrigger>
  <SelectContent>
    <SelectItem value="lan">{t("methodLan")}</SelectItem>
    {grantedFreeboxChannels.length > 0 && (
      <SelectItem value="freebox">{t("methodFreebox")}</SelectItem>
    )}
  </SelectContent>
</Select>
```

`grantedFreeboxChannels` is derived from `useChannels(org)`:

```ts
const { data: channels } = useChannels(org)
const grantedFreeboxChannels = (channels ?? []).filter(
  (c) => c.type === "freebox" && c.settings?.status === "granted"
)
```

The Freebox method option is only rendered when ≥1 granted Freebox channel
exists, so users without a paired Freebox always see LAN as the only method (no
confusing empty state).

### Conditional field sections

- **`method === "lan"` (default):** existing CIDR textarea + advanced toggle
  (ports / timeout / concurrency) — unchanged.
- **`method === "freebox"`:** replace the CIDR section with a channel `Select`
  sourced from `grantedFreeboxChannels`. Label: `t("selectFreeboxChannel")`
  (key already exists). Advanced options are hidden — Freebox scan inherits
  scanner defaults per spec 08.

The **confirmation checkbox** (`t("confirmation")`) is shown for **both**
methods, since Freebox scans are now active probes. Submit is disabled until
checked, same as today.

### Submit logic

```ts
const handleSubmit = async () => {
  if (method === "lan") {
    const result = await startLanScan.mutateAsync({ cidrs, ports?, timeout?, concurrency? })
    navigate({ to: "/orgs/$org/discovery/$jobUid", params: { org, jobUid: result.data.uid } })
  } else {
    const result = await startFreeboxScan.mutateAsync({ channelUid: selectedChannelUid })
    navigate({ to: "/orgs/$org/discovery/$jobUid", params: { org, jobUid: result.data.uid } })
  }
}
```

Both mutations already return `{ data: DiscoveryScan }` with a `uid` field.

## `discovery.index.tsx` changes

File:
[`web/dash0/src/routes/orgs/$org/discovery.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx)

- **Remove** the `FreeboxLauncher` component (lines 98-136) and its
  `useStartFreeboxScan` / `useChannels` usages.
- The "Start new scan" `<Link>` (lines 169-174) and the source filter `Select`
  (`all` / `lan` / `freebox`) remain untouched.

## i18n keys

Add to `web/dash0/src/locales/en/discovery.json` (and mirror in `fr/`, `de/`,
`es/`):

```json
"scanMethod":    "Scan method",
"methodLan":     "IP range (LAN)",
"methodFreebox": "Freebox"
```

Reuse existing keys where possible:
- `selectFreeboxChannel` — channel sub-select label / placeholder.
- `noFreeboxChannels` — not shown in this flow (Freebox option is hidden when no
  granted channels), but may remain for other uses.
- `confirmation` — reused for both methods.
- `freeboxScanStarted` / `freeboxScanFailed` — toast messages on Freebox submit.

The `discoverViaFreebox` key becomes unused; it can be removed when the
`FreeboxLauncher` component is deleted.

## Files affected

| File | Change |
|---|---|
| `web/dash0/src/routes/orgs/$org/discovery.new.tsx` | Add method `Select`, Freebox channel sub-select, conditional field sections, dual submit logic |
| `web/dash0/src/routes/orgs/$org/discovery.index.tsx` | Remove `FreeboxLauncher` component and its associated hooks |
| `web/dash0/src/locales/en/discovery.json` | Add `scanMethod`, `methodLan`, `methodFreebox`; remove `discoverViaFreebox` |
| `web/dash0/src/locales/{fr,de,es}/discovery.json` | Mirror new keys |

No changes to `api/hooks.ts`, `components/`, or any other route.

## Verification

Playwright e2e (`web/dash0/e2e/`) — add or extend the discovery spec:

1. **Default state:** `/discovery/new` loads with method select defaulting to
   LAN; CIDR fields are visible; no Freebox option in the select if no granted
   channel exists.
2. **LAN path unchanged:** fill a CIDR, accept confirmation, submit → scan
   appears in list, navigates to detail page.
3. **Freebox option appears** when a test org has a granted Freebox channel.
4. **Freebox path:** select Freebox method → CIDR fields hidden, channel
   sub-select visible with the paired channel → select channel, accept
   confirmation, submit → navigates to scan detail page.
5. **Removed dropdown:** the "Discover via Freebox" dropdown is no longer present
   on the discovery index page.
6. `make test-dash` passes clean.

## Implementation Plan

1. **i18n keys** — add `scanMethod`, `methodLan`, `methodFreebox` to `en/fr/de/es`
   `discovery.json`; remove the now-unused `discoverViaFreebox` key from all four.
2. **`discovery.new.tsx`** — add a `method` state (`"lan" | "freebox"`) and a
   scan-method `Select` at the top of the form. Derive `grantedFreeboxChannels`
   from `useChannels(org)` filtered by `canSource(c.type) && c.settings?.status
   === "granted"` (mirrors `check-form.tsx`). Render the Freebox option only when
   ≥1 granted channel exists.
   - `method === "lan"`: existing CIDR textarea + advanced options (unchanged).
   - `method === "freebox"`: replace CIDR section with a channel `Select`
     (`selectFreeboxChannel` label) sourced from granted channels; hide advanced.
   - Confirmation checkbox shown for both methods; submit disabled until checked
     and (LAN) a CIDR is entered or (Freebox) a channel is selected.
   - Dual submit: LAN → `useStartDiscoveryScan`; Freebox →
     `useStartFreeboxScan(channelUid)`. Both navigate to
     `/orgs/$org/discovery/$jobUid` using `result.data.uid`. Toasts:
     `scanStarted`/`scanFailed` for LAN, `freeboxScanStarted`/`freeboxScanFailed`
     for Freebox.
3. **`discovery.index.tsx`** — delete the `FreeboxLauncher` component and its
   `useChannels` / `useStartFreeboxScan` / `canSource` / `DropdownMenu` / `Wifi`
   imports and usages. Keep the "Start new scan" link and source filter.
4. **Playwright e2e** — update `discovery.spec.ts`: remove the old "discover via
   Freebox button" test; add coverage for the method select default (LAN), CIDR
   visibility, absence of the index dropdown, and the LAN submit path. Add a
   `data-testid` to the method select where needed for stable selection.
5. **QA** — `rtk make build-backend build-dash0 lint-back test`; iterate to green.
