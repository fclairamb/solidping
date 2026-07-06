# One-click MCP connector setup (Connect-AI page, install links, docs)

## Problem

SolidPing already ships everything hard about being an MCP connector — but a
user who wants to plug it into Claude (or Cursor, VS Code, …) has no obvious
path. The capability is invisible and the docs describe an obsolete, high-
friction flow.

Facts about what already exists (mostly landed with MCP OAuth 2.1 in #85,
`936d8304`):

- **MCP server**: `POST /api/v1/mcp`, JSON-RPC over Streamable HTTP, mounted
  behind `RequireMCPAuth` (`server/internal/app/server.go:553`). Tools cover
  checks, results, incidents, status pages, maintenance, diagnostics; scopes
  `mcp` (read/write) and `mcp:read`.
- **Full OAuth 2.1 stack**, which is exactly what remote-MCP clients
  auto-discover from just the server URL:
  - `/.well-known/oauth-protected-resource` (RFC 9728) and
    `/.well-known/oauth-authorization-server` (RFC 8414, aliased at
    `/.well-known/openid-configuration`) — `server/internal/oauth/metadata.go:28-44`
  - dynamic client registration (`registration_endpoint`,
    `server/internal/oauth/register.go`), PKCE
    (`code_challenge_methods_supported`), JWKS
  - a consent page in the dashboard that defaults to the `mcp` scope and
    renders a read-only variant for `mcp:read`
    (`web/dash0/src/routes/orgs/$org/oauth.consent.tsx:61-62`)

  Net effect: an OAuth-capable MCP client needs **only the URL** — paste
  `https://<host>/api/v1/mcp`, the client registers itself, the user logs in
  and consents, done. No manual token.

The gaps:

1. **The docs are stale and actively add friction.** The docs page
   (`server/internal/app/docsres/docs/features/mcp.md`) predates #85: it
   only documents manually creating a bearer token and pasting it into a
   JSON config with an `Authorization` header. It never mentions that OAuth
   discovery makes all of that unnecessary for Claude/Cursor/VS Code.
2. **No dashboard surface.** Nothing in dash0 tells the user the MCP server
   exists, what the URL is, or how to connect a client. The only adjacent
   surface is the PAT page (`web/dash0/src/routes/orgs/$org/account.tokens.tsx`).
3. **No install affordances.** Clients that support one-click installs
   (Cursor and VS Code deep-link badges) get nothing; clients that need a
   one-liner (Claude Code) or a paste-the-URL flow (claude.ai custom
   connectors) get no copyable snippet.

## Product decision

- Add an **"AI assistants" page** to the dashboard's Organization section:
  `web/dash0/src/routes/orgs/$org/organization.ai.tsx`, sidebar label
  "AI assistants", next to Usage/Settings. This is the single place a user
  goes to connect an AI client.
- The page is built around the **URL-only OAuth flow** (the modern path) and
  offers **the best affordance each client supports** — a true one-click
  deep link where the ecosystem has one, a one-click-copy snippet where it
  doesn't. We do not pretend a universal "install icon" exists; per-client
  buttons are the honest version of "just click an icon".
- PAT + `Authorization` header stays documented as the **fallback for
  headless/scripted agents** only, linking to the existing tokens page.

## Proposal

### 1. Dashboard page `orgs/$org/organization/ai`

Content, top to bottom:

- One-sentence intro + the instance MCP URL
  (`{window.location.origin}/api/v1/mcp`) with a copy button.
- **Client cards** (grid, mobile-friendly), each with an icon and its
  best affordance:

  | Client | Affordance |
  |---|---|
  | Claude (claude.ai / desktop) | Copy-URL button + 3-step inline instructions (Settings → Connectors → Add custom connector → paste URL). No public deep link exists today. |
  | Claude Code | Copy button for `claude mcp add --transport http solidping <url>` |
  | Cursor | "Add to Cursor" deep-link button (`cursor://anysphere.cursor-deeplink/mcp/install?name=solidping&config=<base64 {"url": …}>`) |
  | VS Code | "Add to VS Code" deep-link button (`vscode:mcp/install?<url-encoded JSON>`), plus Insiders variant |
  | Anything else | Collapsible generic JSON config snippet (URL-only first; bearer-token variant second) |

  Deep-link formats must be re-verified against each vendor's current
  published badge format at implementation time — they are client
  conventions, not standards, and may have drifted.
- A short **read-only note**: to grant an assistant read-only access,
  approve the consent screen with the `mcp:read` scope / create an
  `mcp:read` PAT (link to `account.tokens`).
- A **self-hosted caveat** callout: hosted clients (claude.ai) can only
  reach instances that are publicly accessible over HTTPS; local/VPN-only
  deployments should use Claude Code / Cursor / VS Code, which connect from
  the user's machine.

Frontend rules apply: start from the design reference page, reuse shipped
primitives, fully usable on mobile. No backend change is required — the page
only needs the instance origin.

### 2. Docs rewrite (`docs/features/mcp.md`)

Restructure to lead with the URL-only OAuth flow ("paste this URL into your
client — that's it"), mirror the per-client instructions from the dashboard
page, and demote the manual-token flow to a "Headless agents / CI" section.
Document the `.well-known` discovery endpoints so integrators know they
exist. Link the docs page and the dashboard page to each other.

### 3. Phase 2 (separate follow-ups, not in this spec's scope)

- Publish a `server.json` for the SaaS instance to the official MCP registry
  (`registry.modelcontextprotocol.io`) so directory-browsing clients find
  SolidPing without typing a URL.
- "Add to Cursor / VS Code" badges on the marketing site
  (`solidping-website`) and README.
- Anthropic connectors-directory listing for claude.ai — a partnership/
  review process, not a code change.

## Non-goals

- No new auth mechanism — OAuth 2.1 + PAT fallback already cover everything.
- No per-check or per-status-page MCP scoping (scopes stay `mcp`/`mcp:read`).
- No embedded AI assistant in the dashboard (that is
  `specs/ideas/2026-06-02-in-dashboard-ai-assistant.md`).

## Acceptance criteria

- An org user can open **Organization → AI assistants**, copy the MCP URL,
  and connect claude.ai / Claude Code with no manual token creation.
- Cursor and VS Code buttons open the respective apps with the SolidPing
  server pre-filled (correct URL encoded in the deep link).
- Docs `/docs/features/mcp` leads with the URL-only flow; the manual-token
  flow is clearly labeled as the headless fallback.
- Page renders correctly on mobile.

## Testing

- Playwright E2E (`web/dash0/e2e/`): page renders for an org member; the
  copy-URL button yields `<origin>/api/v1/mcp`; Cursor/VS Code anchors
  encode the exact instance URL in their `href` (decode and assert — do not
  click, the protocol handlers aren't available in CI).
- Docs build (`web/docs/`) passes with the rewritten page.
- Backend: no changes, existing `server/internal/oauth/` and
  `server/internal/mcp/` tests already cover the flow the page advertises.
