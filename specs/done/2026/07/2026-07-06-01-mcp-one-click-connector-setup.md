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

## Implementation Plan

Research findings that shape this plan (verified against the current codebase,
not assumed from the spec text):

- **Docs source correction**: the spec references
  `server/internal/app/docsres/docs/features/mcp.md` as the file to rewrite.
  That path is a **build artifact** — populated by `make copy-docs` from the
  Docusaurus build output (`make build-docs`), confirmed via `Makefile`
  `build-docs`/`copy-docs` targets and a byte-level diff of both files
  (identical modulo frontmatter, which Docusaurus strips/promotes at build
  time). The canonical, hand-edited source is
  **`web/docs/docs/features/mcp.md`**. This plan edits only that file; the
  `docsres` copy is regenerated by `make build-docs && make copy-docs` (or
  transitively `make build`) and must not be hand-edited.
- **No shared copy-button/code-block primitive exists yet.** Two inline
  patterns exist in `design-reference.tsx` (`CodeSnippet` at ~L314–343,
  `ReferenceCollapsibleCode` at ~L1451–1501) but neither is exported from
  `@/components/`. Per this repo's mandatory frontend convention, this plan
  extracts a small reusable `CopyableCode` primitive into
  `web/dash0/src/components/shared/copyable-code.tsx` (single-line variant:
  monospace box + copy button, `Copy`/`Check` icon swap via
  `navigator.clipboard.writeText`, matching the existing inline styling) and
  registers it on the design-reference page, rather than inlining a third
  copy of the same JSX.
- **Organization nav** is defined directly in
  `web/dash0/src/routes/orgs/$org/organization.tsx` (`tabs` array) — add one
  entry there. Child pages (e.g. `organization.usage.tsx`) rely entirely on
  the parent layout's admin-only gate; no per-page `beforeLoad`/permission
  check is needed in the new route.
- **PAT scope picker does not exist in the dashboard today.** The backend
  (`CreateTokenRequest.Scopes`) and the generated API client already support
  scoped tokens, but `account.tokens.tsx` / `useCreateToken` only take
  `name`/`expiresAt` — no scope selector, and the CLI (`sp tokens create`)
  has no `--scope` flag either. Adding a scope picker to the tokens page is
  a separate, larger change (new form field, new hook field, new tests) not
  called for in this spec's proposal or acceptance criteria. This plan
  therefore: (a) leads the "read-only access" note with the OAuth consent
  screen's `mcp:read` scope, which is fully working today
  (`oauth.consent.tsx` already renders a distinct read-only variant), and
  (b) mentions PAT scopes as an API-level capability for
  scripted/CLI-driven token creation, without implying the dashboard token
  page has a scope picker (it doesn't — avoid overclaiming in copy).
- **Deep-link formats** (re-verified against current vendor docs/community
  references, since these are unversioned client conventions):
  - Cursor: `cursor://anysphere.cursor-deeplink/mcp/install?name=<name>&config=<base64 JSON>`,
    where JSON for a remote HTTP server is `{"url": "<mcp url>"}` (no `type`
    key needed for HTTP servers per Cursor's own `mcp.json` docs — `type` is
    only required for `stdio`).
  - VS Code: `vscode:mcp/install?<url-encoded JSON>` where JSON is
    `{"name": "<name>", "type": "http", "url": "<mcp url>"}`. VS Code
    Insiders uses the `vscode-insiders:mcp/install?...` scheme. (Some
    community references show `vscode://mcp/install` with `//` — both forms
    are used in the wild; this plan uses the `vscode:mcp/install` form as
    documented on VS Code's own extension-guide/MCP docs page, consistent
    with the no-`//` Cursor scheme.)
  - Both are genuine unversioned client conventions (no formal spec), so the
    E2E test asserts the *encoded URL is correct* (decode base64/URI and
    check the embedded MCP url), not that the link "works" — matching the
    spec's own testing guidance.

### Steps

1. **`CopyableCode` shared primitive** (frontend foundation)
   - Add `web/dash0/src/components/shared/copyable-code.tsx`: small
     component rendering a monospace, single-line (or wrapping) code value
     with a copy-to-clipboard icon button (`Copy`/`Check` swap, ~1500ms
     reset), reusing the existing inline styling conventions
     (`rounded-md border bg-muted/40 px-3 py-2 text-xs font-mono`).
   - Register it on `design-reference.tsx` (new "Copyable code" section)
     with the exact import line, per the mandatory catalog convention.
   - No tests needed standalone (covered indirectly by the new page's E2E).

2. **Route: `organization.ai.tsx`**
   - Add `web/dash0/src/routes/orgs/$org/organization.ai.tsx` mirroring
     `organization.usage.tsx`'s structure (`Card`/`CardHeader`/`CardTitle`/
     `CardDescription`/`CardContent`, `useTranslation(["org"])`,
     `Route.useParams()` for `org`). No data hook needed — content is static
     modulo `window.location.origin`.
   - Content top to bottom: intro sentence + MCP URL via `CopyableCode`
     (`{origin}/api/v1/mcp`); responsive card grid (`grid gap-4 sm:grid-cols-2
     lg:grid-cols-3` or similar) with one card per client:
     - Claude (web/desktop): 3-step instructions + `CopyableCode` for the URL.
     - Claude Code: `CopyableCode` for
       `claude mcp add --transport http solidping <url>`.
     - Cursor: deep-link `<a>` button, `href` built from `name`+base64 config
       per the verified format above.
     - VS Code: deep-link `<a>` button (+ small Insiders variant link/note),
       `href` built from url-encoded JSON per the verified format above.
     - Generic/other: `ReferenceCollapsibleCode`-style collapsible JSON
       snippet, URL-only variant first, bearer-token variant second (mirrors
       the docs' example, sourced from the existing
       `Example Client Configuration` JSON block).
   - Read-only note: OAuth consent `mcp:read` scope (works today) as primary
     read-only path; secondary mention of PAT `scopes` for API/CLI-created
     tokens, linking to `/orgs/$org/account/tokens`.
   - Self-hosted caveat: `Alert variant="warning"` — hosted clients need a
     publicly reachable HTTPS URL; local/VPN-only installs should use
     Claude Code/Cursor/VS Code instead.
   - Mobile: verify grid stacks to 1 column below `sm`, buttons/copy targets
     stay tappable (no fixed widths).
   - Cross-link to the docs page (`/docs/features/mcp`).

3. **Nav wiring**
   - Add the "AI assistants" tab entry to `organization.tsx`'s `tabs` array,
     positioned next to Usage/Settings per the spec.
   - Add the corresponding i18n keys (`nav:ai` or similar, plus `org:ai.*`
     content keys) to whatever locale files back `useTranslation(["org",
     "nav"])` (mirror however `usage.*`/`nav:usage` keys are structured).

4. **Docs rewrite**: `web/docs/docs/features/mcp.md`
   - Restructure to lead with "paste this URL into your client" (URL-only
     OAuth flow): endpoint URL, then per-client instructions mirroring the
     dashboard page (Claude, Claude Code, Cursor, VS Code, generic/other).
   - Document the `.well-known` discovery endpoints (`oauth-protected-resource`,
     `oauth-authorization-server`/`openid-configuration`, dynamic client
     registration) so integrators know OAuth auto-discovery is why the
     URL-only flow works.
   - Demote the manual bearer-token / `Example Client Configuration` section
     to a "Headless agents / CI" heading, keep the existing scopes table
     under it (still accurate/needed).
   - Add a cross-link to the new dashboard page
     (`/orgs/$org/organization/ai`, phrased generically since docs aren't
     org-scoped) and add a matching "See also" link from the dashboard page
     back to `/docs/features/mcp`.
   - Do NOT touch `server/internal/app/docsres/docs/features/mcp.md` — it
     regenerates via `make build-docs && make copy-docs`.

5. **E2E test**: `web/dash0/e2e/organization-ai.spec.ts`
   - Page renders for an authenticated org member (heading visible).
   - Copy-URL control exposes/produces `<origin>/api/v1/mcp` (assert via the
     rendered text content next to the copy button, since clipboard access
     in CI Playwright is possible but the simpler/more robust assertion is
     the visible URL text — use clipboard-read assertion only if trivial,
     otherwise assert the displayed string).
   - Cursor anchor `href`: decode the `config` query param (base64 → JSON)
     and assert `url === "<origin>/api/v1/mcp"`.
   - VS Code anchor `href`: decode the URL-encoded JSON and assert
     `url === "<origin>/api/v1/mcp"`.
   - Do NOT click either deep-link anchor.

6. **QA pass**: `make build-dash0`, `cd web/dash0 && bun run lint`,
   `make build-docs`; fix any new errors in touched files only (do not fix
   pre-existing eslint debt).

### Explicitly out of scope (confirmed, not re-litigating)

- Adding a scope picker to `account.tokens.tsx` (see research note above).
- Phase 2 items (server.json registry, marketing badges, Anthropic
  connectors-directory listing) — per spec's own Non-goals/Phase 2 section.
- Any backend change.
