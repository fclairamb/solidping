# Freebox LAN discovery

## Context

Depends on [`2026-05-24-06-freebox-os-integration.md`](2026-05-24-06-freebox-os-integration.md).
Build this only after the foundation and line-quality check (spec 2 and 3) have shipped.

The Freebox OS API exposes a LAN host browser: a list of all devices currently known to the
Freebox (by MAC address), including their IP, hostname, last-activity timestamp, and reachability.
This is a convenient shortcut for home-network users who want to monitor their own devices but
do not know their hostnames or IPs by heart.

This spec defines a "Discover from Freebox" flow that queries the LAN browser and lets the user
promote one or more hosts into ICMP (ping) checks without typing anything.

## Honest opinion

This is a quality-of-life feature, not a load-bearing capability. The solidping user can already
create ICMP checks manually — discovery just removes the friction of looking up an IP. Given that:

- Ship this only after specs 1+2 are done and feel stable.
- Keep the implementation thin: read-only, no passive monitoring of the host list (see Non-goals).
- Do not model this as a "sync" that keeps check config in step with Freebox host data.
  The user creates a check once; after that it's independent. Freebox and solidping should not
  be tightly coupled.
- The real value here is the "Discover" UX shortcut, not any ongoing integration.

## Goal

- A "Discover from Freebox" picker in the new-check flow (or check list page) that queries
  `/api/v4/lan/browser/` and returns known LAN hosts.
- User selects one or more hosts → solidping pre-fills an ICMP check with the host's IP and
  a friendly name derived from its hostname.
- The check is created normally; no link is maintained between the check and the Freebox host.

## Non-goals

- Passive monitoring: alerting when a known Freebox host goes offline (too noisy for home networks
  where devices sleep regularly). This *could* be built on top of heartbeat semantics later but
  is explicitly out of scope here.
- Syncing the Freebox host list on a schedule.
- Showing Freebox host metadata on the check detail page after creation.
- Anything beyond ICMP checks (e.g., auto-creating HTTP checks for discovered hosts).

## Freebox LAN browser API

```
GET /api/v4/lan/browser/pub/
→ [
    {
      "id":              "ether-aa:bb:cc:dd:ee:ff",
      "primary_name":    "macbook-pro",
      "host_type":       "workstation" | "laptop" | "smartphone" | "tablet" | "printer" | "player" | "nas" | "ip_camera" | "router" | "other",
      "l3connectivities": [
        {
          "addr":        "192.168.1.10",
          "af":          "ipv4",
          "active":      true,
          "reachable":   true,
          "last_activity": 1716542400
        }
      ],
      "last_time_reachable": 1716542400,
      "active":          true,
      "reachable":        true,
      "names":           [{"name": "macbook-pro", "source": "dhcp"}, {"name": "mac.local", "source": "mdns"}]
    }, ...
  ]
```

The `pub` interface is the main wired/WiFi LAN. The router itself (`host_type: "router"`) appears
in the list — filter it out of the picker since creating a Freebox→Freebox ICMP check is not useful.

## API endpoint

```
GET /api/v1/orgs/:org/integrations/freebox/:connectionUid/lan-hosts
```

Returns a filtered, simplified list for the frontend:

```json
{
  "data": [
    {
      "id":         "ether-aa:bb:cc:dd:ee:ff",
      "name":       "macbook-pro",
      "ip":         "192.168.1.10",
      "hostType":   "laptop",
      "reachable":  true,
      "lastSeen":   "2026-05-24T10:00:00Z"
    }
  ]
}
```

Filter rules:
- Exclude hosts with no active IPv4 connectivity.
- Exclude `host_type == "router"` (the Freebox itself).
- Prefer the most recently active IPv4 address for hosts with multiple IPs.
- Sort: reachable first, then by name.

## Frontend

### "Discover from Freebox" button

On the new-check page (ICMP check form), if the org has at least one `granted` Freebox connection,
show a secondary action button: **"Discover from Freebox"**. Clicking it opens a modal (or
inline panel) with the host list.

```
┌─────────────────────────────────────────────────────┐
│  Discover hosts from Freebox                        │
│                                                     │
│  [🔍 Search...                              ]       │
│                                                     │
│  ● macbook-pro           192.168.1.10  laptop  ●   │
│  ○ nas-synology          192.168.1.20  nas     ●   │
│  ○ living-room-tv        192.168.1.30  player  ○   │ ← unreachable
│  ○ old-pi                192.168.1.40  other   ○   │
│                                                     │
│  [Cancel]              [Create 1 check →]          │
└─────────────────────────────────────────────────────┘
```

Single-select: picks one host, pre-fills the ICMP check form with:
- `name`: the host's `primary_name` (user can edit before saving)
- `config.host`: the host's IPv4 address

The user then reviews and saves normally. No special handling needed in the backend — it's just
a regular ICMP check creation.

Check `http://localhost:4000/dash0/orgs/default/design-reference` for the modal and searchable
list primitives before implementing anything custom.

### Connection selector

If the org has multiple Freebox connections, show a connection selector above the host list.

### i18n

`web/dash0/src/locales/en/checks.json` and `fr/checks.json`:
- `"freebox.discover"` — "Discover from Freebox"
- `"freebox.discoverTitle"` — "Discover hosts from Freebox"
- `"freebox.hostType.workstation"` — "Desktop"
- `"freebox.hostType.laptop"` — "Laptop"
- `"freebox.hostType.smartphone"` — "Phone"
- `"freebox.hostType.nas"` — "NAS"
- `"freebox.hostType.printer"` — "Printer"
- `"freebox.hostType.player"` — "Media player"
- `"freebox.hostType.ip_camera"` — "Camera"
- `"freebox.hostType.other"` — "Device"

## Files to create / modify

### New files
- `server/internal/handlers/channels/freebox_lan.go` — `LanHostsHandler` (GET handler)

### Modified files
- `server/internal/handlers/channels/handler.go` — register the `/lan-hosts` route
- `server/internal/integrations/freebox/service.go` — add `ListLanHosts(ctx, client) ([]LanHost, error)`
- `server/internal/integrations/freebox/types.go` — add `LanHost` and Freebox LAN browser response types
- `web/dash0/src/components/shared/check-form.tsx` — add "Discover from Freebox" button for ICMP type
- `web/dash0/src/locales/en/checks.json` — i18n
- `web/dash0/src/locales/fr/checks.json` — i18n

## Tests

### Backend unit test (`freebox_lan_test.go`)

Mock the Freebox LAN browser response:
- Assert router host is filtered out.
- Assert hosts with no active IPv4 are filtered out.
- Assert sort order (reachable first, then by name).
- Assert the simplified `LanHost` shape returned to the frontend.

### Playwright

- With a mocked Freebox connection (or in test mode with a stub), click "Discover from Freebox"
  and confirm the picker appears with at least one host.
- Select a host and confirm the ICMP check form is pre-filled with the correct IP and name.

## Verification

```bash
make lint && make test
```

Manual (requires a paired Freebox from spec 1):

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://localhost:4000/api/v1/orgs/default/integrations/freebox/<connectionUid>/lan-hosts" | jq .
```

Confirm the router itself does not appear in the list. Confirm a phone on the network appears with
`reachable: true` when it is active and `false` when it is asleep.
