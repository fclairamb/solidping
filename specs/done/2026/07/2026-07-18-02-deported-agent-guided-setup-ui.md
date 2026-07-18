---
model: sonnet
effort: medium
---

# Registering a deported agent has no guided setup — the UI mints a token and leaves you on your own

## Problem

The private-locations page
(`web/dash0/src/routes/orgs/$org/organization.private-locations.tsx`, route
`/orgs/$org/organization/private-locations`) already covers the raw
mechanics of deported agents: create a private region, mint an `spe_`
enrollment token (secret shown once), list/revoke agents. The backing API is
complete (`server/internal/app/server.go:790-797` for the admin routes,
`GET /api/v1/agent/ws` at `server.go:819` for the agent transport).

But registering an agent is a **multi-step, cross-machine procedure** and the
UI explains none of it. After minting a token the user gets a bare secret and
has to go find `web/docs/docs/features/private-locations.md` to learn:

- which env vars the agent needs (`SP_NODE_ROLE=agent`, `SP_AGENT_SERVER_URL`,
  `SP_AGENT_ENROLLMENT_TOKEN`, optional `SP_AGENT_NAME` / `SP_AGENT_KEYS_FILE`
  / `SP_AGENT_KEYS` — see `server/internal/config/envvars.go:130-134`);
- what command to actually run (docker run / compose / Kubernetes env-only-keys
  pattern from the docs page);
- that the token is one-shot and the agent keeps its own Ed25519 keypair, so
  the keys file must be persisted across restarts;
- how to tell whether enrollment worked (the agent appearing in the agents
  list) and what to do next (target the private location from a check).

The result: registering an agent requires juggling the dashboard, the docs
site, and a terminal, and it's easy to mint a token, lose the secret, and not
know why the agent never shows up.

## Proposal

Add a **guided "Register an agent" flow** to the private-locations page — a
dedicated route (per repo convention, editing/creation navigates to a route,
never a modal), e.g.
`/orgs/$org/organization/private-locations/register`, reachable from a
prominent button on the list page and from the empty state.

The flow walks through the real procedure, generating copy-pasteable
artifacts from live state:

1. **Pick or create the private location** — reuse the existing
   create-region form inline (name + slug pair from the design reference).
2. **Mint the enrollment token** — calls the existing
   `useMintEnrollmentToken` hook (`web/dash0/src/api/hooks.ts`); show the
   secret once with a copy button and a clear "you won't see this again"
   note, plus the TTL (default 24h, `server/internal/handlers/agents/service.go:22`).
3. **Run the agent** — render ready-to-copy setup snippets with the server
   URL (derive from `window.location.origin`) and the freshly minted token
   already substituted, as tabs: `docker run`, `docker compose`, and
   Kubernetes (env-only keys via `SP_AGENT_KEYS`). Content should mirror —
   not fork — `web/docs/docs/features/private-locations.md`; include a link
   to that docs page (`/docs/features/private-locations`) for the full
   reference. Explain the two gotchas inline: the token is single-use, and
   the keys file (`/data/agent-keys.json` by default) must be persisted.
4. **Wait for the agent to connect** — poll the existing agents list
   endpoint (`GET /api/v1/orgs/:org/agents`) and flip to a success state
   when a new agent enrolls into the chosen region, showing its name and a
   pointer to the next step ("target this location from a check"). If the
   token expires or is consumed elsewhere, surface that instead of spinning
   forever.

Constraints and notes:

- Frontend-only: every API needed already exists; no backend change expected.
- Follow the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) for steppers /
  code-block-with-copy primitives; if a "copyable command block" primitive
  doesn't exist yet, add it to the reference page as part of the change.
- Must be usable on mobile (the user is likely reading the dashboard on one
  screen and a terminal on another).
- E2E: a Playwright test in `web/dash0/e2e/` driving the wizard far enough to
  verify snippet generation contains the minted token and org-correct URL
  (the "agent connects" step can be exercised by hitting the enrollment
  consumption path the way existing agent tests do, or asserted at the
  waiting state).

Open question (don't invent scope): whether step 3's snippets should also
cover the plain-binary (non-container) install; the docs page is the source
of truth — include what it documents, no more.

## Implementation Plan

Scope confirmed frontend-only (`web/dash0`); every hook needed already exists
in `src/api/hooks.ts` (`usePrivateRegions`, `useCreatePrivateRegion`,
`useMintEnrollmentToken`, `useAgents`, `useEnrollmentTokens`). No backend
change.

### 1. `Stepper` UI primitive
Add `web/dash0/src/components/ui/stepper.tsx` — no stepper primitive exists
today (checked design-reference.tsx + repo-wide grep). Horizontal row of
`{index, label}` steps, each a circle (check icon when done, filled number
when active, outline number when upcoming) linked by a connector line;
responsive via `flex-wrap` so labels stack under circles on narrow screens
instead of overflowing. Add a "Stepper" section to
`design-reference.tsx` (new `SECTIONS` entry + section function) showing the
3 states, per the mandatory design-reference-stays-canonical convention.
`CopyableCode` (in `components/shared/copyable-code.tsx`) already covers the
"copyable command block" need — reused as-is for step 3, no new primitive
needed there.

### 2. `useAgents` poll-rate override
Extend `useAgents(org, options?: { refetchInterval?: number; enabled?: boolean })`
in `hooks.ts`, defaulting to today's `30_000`/`true` so the one existing
caller (`AgentsCard`) is unaffected. The wizard's step 4 passes a faster
interval (4s) while waiting, and disables the poll once step 4 is left or a
terminal (success/error) state is reached.

### 3. Wizard route
New file `web/dash0/src/routes/orgs/$org/organization.private-locations.register.tsx`,
route `/orgs/$org/organization/private-locations/register`. `validateSearch`
carries only `regionSlug?: string` (so the entry points below can preselect
a location) — the step index and the minted secret stay in component state,
never the URL: a reload always loses the one-shot secret anyway (it can't be
redisplayed), so encoding "step" in the URL would just produce a broken deep
link, and the secret must never be serialized into history/URL regardless.

Steps (local `step` state, 1-4):
1. **Pick or create location** — list existing `usePrivateRegions(org)` as
   selectable rows; selecting one sets `regionSlug` (search param) and
   advances. A `CollapsibleSection` "Or create a new location" (open by
   default when there are zero existing regions) hosts an inline
   name+slug form (name first, auto-slugify per the canonical convention —
   the older inline form on the main private-locations page predates that
   convention and is left as-is, out of scope) using `useCreatePrivateRegion`;
   success advances with the new slug.
2. **Mint token** — `useMintEnrollmentToken(org)`, a "Mint enrollment token"
   button. On success, store the `MintedEnrollmentToken` in component state
   (not the query cache is fine — already invalidated by the hook), render
   secret + copy button (`CopyableCode`), a clear "shown only once" callout,
   and the TTL derived from `minted.expiresAt` (defaults to 24h per
   `service.go:22`). "Continue" advances.
3. **Run the agent** — `Tabs` with `docker run` / `docker compose` /
   Kubernetes (env-only `SP_AGENT_KEYS`), content mirroring
   `web/docs/docs/features/private-locations.md` verbatim (server URL from
   `window.location.origin`, token substituted), link to
   `/docs/features/private-locations`, two `Alert` callouts (single-use
   token; `/data/agent-keys.json` must persist). "Continue — I started the
   agent" advances.
4. **Wait for connection** — snapshot existing agent uids in the target
   region on entry; poll `useAgents(org, { refetchInterval: 4000 })` for a
   new uid in that region → success card (agent name + link to
   `/orgs/$org/checks/new`). Poll `useEnrollmentTokens(org)` in parallel;
   surface an error state (with a "mint a new token" retry back to step 2)
   when `minted.expiresAt` has passed, or when the token's uid drops out of
   the live list without a matching new agent appearing (consumed
   elsewhere/cancelled).

### 4. Wire up entry points
In `organization.private-locations.tsx`: add a prominent "Register an agent"
button (header actions) and reword the empty state to link into the wizard,
both navigating to the new route. Leave the existing raw mint/create
mechanics in place for power users — additive only.

### 5. E2E
`web/dash0/e2e/deported-agent-wizard.spec.ts`: create/clean up via API like
`private-locations.spec.ts`; drive step 1 (pick existing region), step 2
(mint), assert step 3's snippets contain the minted `spe_` token and the
correct server URL; assert step 4 renders the waiting state. Exercising a
real agent WS enrollment from Playwright isn't practical (protocol-level,
not HTTP) — waiting-state assertion per the spec's explicit allowance.

### 6. QA
`make build-dash0`, `cd web/dash0 && bun run lint` (no new errors), targeted
Playwright run of the new spec file.
