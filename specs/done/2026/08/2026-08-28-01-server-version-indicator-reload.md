---
model: sonnet
effort: medium
---

# Dash0 has no way to tell the user their loaded app is older than the running server

## Problem

When the server is redeployed, browsers that already have the dash0 SPA loaded keep
running the old bundle until the user happens to reload. There is no indication anywhere
in the UI that the client is stale, and no display of the running server version at all.

What exists today:

- The server exposes `GET /api/mgmt/version` (unauthenticated, handled by
  `getVersion` in `server/internal/app/server.go:2314`), returning
  `{"version":"...","commit":"...","gitTime":"...","runMode":"..."}` from
  `server/internal/version/version.go` (ldflags-injected; `"dev"` when untagged).
- The frontend reads `import.meta.env.VITE_APP_VERSION || "dev"`
  (`web/dash0/src/components/feedback/useFeedback.ts:136`) — but **nothing ever sets
  `VITE_APP_VERSION`** (not `vite.config.ts`, not the Makefile, not CI), so the client
  has no reliable knowledge of its own build version.
- Since dash0 is embedded in the Go binary, the server's version *is* the version of
  the assets it serves: "client differs from server" ⇔ "the server was redeployed
  after this page loaded".

## Proposal

Add a discreet version indicator in the top-left of the dashboard (the sidebar header
area, `SidebarHeader` in `web/dash0/src/components/layout/AppSidebar.tsx:168`, near the
`Logo`), with a staleness poll:

1. **Client version identity.** On app load, fetch `/api/mgmt/version` once and record
   the result as the *loaded* version (this sidesteps the unset `VITE_APP_VERSION` —
   the embedded SPA is by construction the same version as the server that served it).
   Optionally also wire `VITE_APP_VERSION` properly at build time (vite `define` from
   the release tag) as a secondary/dev signal, but the boot-time snapshot is the
   authoritative baseline.
2. **Poll every 15 minutes.** Re-fetch `/api/mgmt/version` on a 15-minute interval
   (and on window focus/`visibilitychange` regain, so a laptop waking from sleep
   doesn't wait up to 15 more minutes). Failures are silent — never surface an error
   for this background poll.
3. **Display.**
   - Normal state: the current server version rendered small and muted (e.g.
     `v0.19.1`), discreet — think `text-xs text-muted-foreground` next to/under the
     logo. In dev (`"dev"` version) it can render as `dev` or be hidden.
   - Stale state (fetched version ≠ loaded version): additionally show the loaded
     client version and a **red reload icon** (`RefreshCw`/`RotateCw` in
     `text-destructive`) that performs `location.reload()` on click. A tooltip should
     explain, e.g. "Server updated to v0.19.2 — reload to get the latest version."
4. **Conventions.** Follow the design reference
   (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) for the tooltip/button
   primitives; keep it usable on mobile (the sidebar collapses — the indicator must not
   break the collapsed/sheet layouts). Note the repo rule that destructive red is
   reserved for destructive actions — the red here is an explicit product request for
   attention on the reload affordance, per the description; keep it on the icon only.
5. **Tests.** A Playwright test (or component test) covering: version rendered;
   mismatch → red reload icon appears; click → reload triggered. Mock
   `/api/mgmt/version` responses to simulate the redeploy.

## Open questions

- Exact placement: "top left" in the description most plausibly means the sidebar
  header next to the logo; if that area is too crowded, the `SidebarFooter`
  (`AppSidebar.tsx:284`) is the common alternative for version strings — implementer
  picks whichever stays discreet in both expanded and collapsed states.
- Whether status0 (public status pages) should get the same treatment is **out of
  scope** — this spec is dash0 only.
