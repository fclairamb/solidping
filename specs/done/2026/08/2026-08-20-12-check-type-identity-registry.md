---
model: sonnet
effort: medium
---

# Check types have no canonical visual identity — 5 hand-rolled badge tints out of 40 types, no icons, duplicated color maps

## Problem

Check types are the primary vocabulary of the product, but their visual identity is
thin, duplicated, and stops at 5 of the 40 creatable types:

- `ProtocolBadge` in
  [checks.index.tsx:407-449](../../web/dash0/src/routes/orgs/$org/checks.index.tsx)
  is a hard-coded if-chain that tints exactly five families — http/https → blue,
  tcp → cyan, dns → amber, icmp/ping → purple, tls/ssl → emerald — and renders the
  other ~35 types (`ssh`, `postgresql`, `kafka`, `heartbeat`, …) as a plain outline
  badge.
- The same five tints are **duplicated** as `PROTOCOL_BADGE_TONES` in
  [design-reference.tsx:2810-2816](../../web/dash0/src/routes/orgs/$org/design-reference.tsx),
  maintained by hand in parallel.
- The check detail page renders the type as **plain text** (`{check.type}`,
  [checks.$checkUid.index.tsx:814](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)
  and a `capitalize` div at line 1177) — no badge, no tint, no anchor visual.
- The new-check type picker (searchable combobox in
  [check-form.tsx:915-985](../../web/dash0/src/components/shared/check-form.tsx),
  fed by the `checkTypes` label/description list at line 77) renders 40 rows of
  text with no visual differentiation at all.
- No check type has an icon anywhere in the product.

There is already a shipped precedent for doing this right: `EVENT_TYPE_REGISTRY` in
[event-display.tsx](../../web/dash0/src/components/dashboard/event-display.tsx) is
the one canonical emoji+tone registry for event types, consumed everywhere. And
[check-type-docs-anchors.ts](../../web/dash0/src/components/shared/check-type-docs-anchors.ts)
shows the drift-guard pattern for per-type maps (a test fails the build when the map
diverges from the backend registry).

## Proposal

Create **one canonical check-type identity registry** and drive every check-type
rendering from it. Lucide icons ship now; internally designed SVGs slot in later
without touching any call site.

### 1. The registry

New module `web/dash0/src/components/shared/check-type-identity.tsx`:

```ts
export interface CheckTypeIdentity {
  label: string;          // "HTTP", "PostgreSQL", …
  tone: string;           // Tailwind tint classes (bg-*/10 text-*-700 dark:text-*-400 border-*/25)
  icon: LucideIcon | FC<SVGProps<SVGSVGElement>>;  // custom-SVG slot-in later
}

export const CHECK_TYPE_IDENTITY: Partial<Record<string, CheckTypeIdentity>> = { … };
export function getCheckTypeIdentity(type: string | undefined): CheckTypeIdentity; // never returns undefined — falls back
```

- **Keyed by the raw backend check-type string** (like the docs-anchors map, and
  for the same reason — the backend registry
  `server/internal/checkers/checkerdef/types.go` is the source of truth and can
  carry types like `kubernetes` that aren't creatable in the form yet). It also
  handles the aliases `ProtocolBadge` handles today (`https`, `ping`, `ssl`/`tls`).
- **Fallback rule (unchanged in spirit):** an unknown type gets the plain outline
  badge, its raw name as label, and a neutral `Activity` icon — never an invented
  sixth style.
- Export a `CheckTypeBadge` component (the current 10px mono uppercase chip,
  registry-driven) so the `ProtocolBadge` if-chain and the design-reference copy
  both collapse into it.

### 2. Tones: keep the shipped five, extend by family

40 distinguishable hues don't exist; assign tones **per family**, keeping the five
already shipped exactly as-is (they're in users' muscle memory):

| Family | Types | Tone |
|---|---|---|
| Web | http/https, websocket, browser | blue *(shipped)* |
| Raw network | tcp, udp, ntp, snmp | cyan *(shipped)* |
| Naming | dns, domain, dnsbl | amber *(shipped)* |
| Reachability | icmp/ping | purple *(shipped)* |
| Certificates | ssl/tls | emerald *(shipped)* |
| Remote access | ssh, sftp, ftp, rdp | teal |
| Mail | smtp, pop3, imap, email | rose |
| Databases | postgresql, mysql, mssql, oracle, clickhouse, redis, mongodb | indigo |
| Messaging/RPC | grpc, kafka, mqtt, rabbitmq | fuchsia |
| Game | a2s, minecraft | lime |
| Infra | docker, prometheus, freebox_line, kubernetes | sky |
| Scripted/synthetic | js, sleep, heartbeat, sip | slate |

Each tone uses the existing class shape
(`bg-{hue}-500/10 text-{hue}-700 dark:text-{hue}-400 border-{hue}-500/25`). The
table is a **default** — the implementer may re-shuffle family membership if a
type reads better elsewhere, but the five shipped assignments must not change,
and no new hue may collide with a status color (green=ok / red=down semantics
stay reserved).

### 3. Icons: Lucide now, custom SVG later

Per-type Lucide suggestions (defaults, adjust where a better glyph exists):
`Globe` http · `Cable` websocket · `AppWindow` browser · `EthernetPort` tcp ·
`Radio` udp · `SquareTerminal` ssh · `FolderLock` sftp · `FolderOpen` ftp ·
`Radar` icmp · `Send` smtp · `Inbox` pop3/imap · `MailCheck` email ·
`Signpost` dns · `CalendarClock` domain · `ShieldAlert` dnsbl ·
`Database` postgresql/mysql/mssql/oracle · `DatabaseZap` redis · `Leaf` mongodb ·
`Rabbit` rabbitmq · `ChartColumn` clickhouse · `Workflow` grpc · `Logs` kafka ·
`Rss` mqtt · `Gamepad2` a2s · `Pickaxe` minecraft · `Router` snmp ·
`Flame` prometheus · `Container` docker · `Wifi` freebox_line · `Lock` ssl ·
`Clock` ntp · `MonitorDot` rdp · `PhoneCall` sip · `FileCode` js · `Moon` sleep ·
`HeartPulse` heartbeat · `Activity` fallback.

The `icon` field's type deliberately accepts any component rendering an SVG that
inherits `currentColor` and a square viewBox. When internally designed icons
arrive (24×24 grid, ~2px stroke, `stroke="currentColor"` — i.e. Lucide-compatible
so they sit next to the rest of the app), they replace entries in this one file
and every call site updates for free. That icon-set commission is **out of scope**
here; this spec builds the slot.

### 4. Apply it (this spec's call sites)

1. **New-check type picker** ([check-form.tsx:945-981](../../web/dash0/src/components/shared/check-form.tsx)):
   each combobox row gets a leading icon in the type's tone color (~`h-4 w-4`).
   Keep the search, labels, descriptions, and `data-testid` untouched. (With 40
   types, a card grid would hurt more than help — not doing that.)
2. **Check detail header** ([checks.$checkUid.index.tsx:814](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)):
   replace the plain-text type with the registry's icon + `CheckTypeBadge`; same
   for the plain `capitalize` div at line 1177.
3. **Checks list** ([checks.index.tsx:407-449](../../web/dash0/src/routes/orgs/$org/checks.index.tsx)):
   replace the `ProtocolBadge` if-chain with `CheckTypeBadge`. The list chip
   **stays text-first — no icon inside the 10px badge**; at that size an abstract
   glyph is noise, and the acronym is the signal. The registry makes adding a
   leading glyph later a one-line change if we ever want it.
4. **Design reference** ([design-reference.tsx:2807-2846](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)):
   the Protocol badge section renders from the registry (delete the local
   `PROTOCOL_BADGE_TONES` copy), documents the family→tone table and the icon
   slot-in contract, and shows the fallback. Sweep other pages for stray copies of
   the tint-class strings and consolidate any that describe check types.

Accessibility stance is unchanged and should stay documented in the reference:
tint and icon are decoration; the text label always spells the type out.

### 5. Drift guard

`check-type-identity.test.ts` (vitest, like
[event-display.test.ts](../../web/dash0/src/components/dashboard/event-display.test.ts)):
every member of the `CheckType` union in
[common.ts:10-50](../../web/dash0/src/components/checks/form/types/common.ts) and
every key of `checkTypeDocsAnchors` must have a registry entry (so a new check
type fails the build until it gets an identity), and every entry's tone string
must match the expected class shape.

### Out of scope

- Commissioning/drawing the custom icon set itself.
- status0 (public status page) and docs-site usage — the registry is importable
  from there later, but no changes now.
- Backend changes. This is dash0-only.
