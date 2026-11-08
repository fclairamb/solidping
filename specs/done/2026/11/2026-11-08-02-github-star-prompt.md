---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Ask users, discreetly and once, to star the GitHub repo from inside the product

## Problem

GitHub stars are how self-hosters and directory curators judge whether an open-source monitor is alive. Nothing in the product mentions the repository: dash0's sidebar footer (`web/dash0/src/components/layout/AppSidebar.tsx:296-409`) holds the language switcher, theme toggle, live dot, version indicator and the user menu, and the server logs nothing about it at startup. People who get value from SolidPing, self-hosters above all, are never invited to say so.

The ask must stay discreet and honest: one permanent link, one prompt shown once after a positive moment (never at signup), the same on self-hosted instances, a single operator switch to turn all of it off, and no reward of any kind.

## Proposal

### 0. Decisions taken in this spec (approved by Florent, 2026-11-08)

- **Trigger: the org has had an enabled check for at least 7 days, the user is back on the dashboard home, and nothing is currently down.** Why this one and not the alternatives:
  - It costs one aggregate column on a query the dashboard home already polls (`GET /api/v1/orgs/:org/checks/stats`, see step 2). No new endpoint, no new table, no migration.
  - It is reliable: it holds for every retained user, including ones whose checks never fail. "First incident resolved" or "first alert received" only fire for users whose target broke, and the resolve moment usually happens while the user is not in the dashboard (the alert reaches them by email/Slack). The incident detail page is also the worst place to ask a favour.
  - Seeing the dashboard 7+ days after the first check *is* the positive moment: it is rung 5 (retained) of the adoption ladder. Existing long-time users qualify on their next visit, with no backfill.
  - The checks list cannot provide this cheaply: `GET /checks` is ordered `created_at DESC` by default and only accepts `sort=group|targetHost` (`server/internal/handlers/checks/handler.go:355-366`), so the oldest check sits on the last page.
- **Persistence: the existing per-user `ui-state` store**, server-side, so the prompt never comes back on another device. It is the store built for exactly this (the getting-started checklist dismissal, `server/internal/handlers/uistate/service.go:1-16`), rows in `state_entries` scoped by `user_uid`. The key is user-global (not per org): a user is asked once in their life, not once per org.
- **"Not now" is final.** Once the user clicks either button or the close icon, the prompt never shows again, on any device, in any org. A prompt that is displayed but ignored (the user navigates away) keeps showing on the dashboard home on later visits, but only for 14 days after it was first shown; after that it is treated as dismissed without a write from the user. `github_star_prompt_shown` fires once per user (on the first display), not on every render.
- **Never in a demo session** (`user.demo`, `web/dash0/src/contexts/AuthContext.tsx:254-275`): the demo account is shared by every visitor, so its ui-state would be shared too. The sidebar link still shows there.
- **Operator switch: `SP_APP_STARPROMPT`, default `true`.** It sits in `AppConfig` next to the bug-report settings (`server/internal/config/config.go:1223`, defaults literal at `:2342`). koanf's env provider maps each underscore to a dot (`server/internal/config/envvars.go:73-80`), so the leaf must be one underscore-free word: `app.starprompt` is reachable as `SP_APP_STARPROMPT`, `app.star_prompt` would not be. `false` hides the sidebar link and the prompt and silences the server log line and the CLI lines.

### 1. Config and public config

1. `server/internal/config/config.go`: add `StarPrompt bool \`koanf:"starprompt"\`` to `AppConfig` (`:1223`) with a comment, and `StarPrompt: true` in the defaults literal (`:2342`). `TestRecognizedEnvVars` / `TestEverySPEnvVarReadIsRecognized` (`server/internal/config/envvars_test.go:48,209`) must pass with the new name.
2. `server/internal/handlers/publicconfig/handler.go`: add `GitHubStar GitHubStarPublicConfig \`json:"githubStar"\`` to `Response` (`:175`), with `type GitHubStarPublicConfig struct { Enabled bool \`json:"enabled"\` }`, filled from `cfg.App.StarPrompt` in `GetConfig` (`:273`). Same pattern as `BugReportPublicConfig` (`:140-145`).
3. `web/dash0/src/lib/analytics.ts`: add `githubStar?: { enabled: boolean }` to `PublicConfig` (`:150-170`). Read it through `web/dash0/src/api/public-config.ts` like `useRunMode`. **Missing means on** (an older server that does not send the key still gets the link; the backend default is on too).
4. Document `SP_APP_STARPROMPT` in `web/docs/docs/configuration/index.md`, next to the other `SP_APP_*` variables.

### 2. The trigger signal: oldest enabled check

1. `server/internal/db/models/check.go:212`: add `OldestCreatedAt *time.Time \`bun:"oldest_created_at"\`` to `CheckStatusCount`.
2. `GetCheckStatusCounts` in `server/internal/db/postgres/postgres.go:6552` and `server/internal/db/sqlite/sqlite.go:6512`: add `ColumnExpr("MIN(created_at) AS oldest_created_at")` to the existing `GROUP BY status, enabled` query. No extra query.
3. `server/internal/handlers/checks/stats.go`: add `OldestEnabledCheckCreatedAt *time.Time \`json:"oldestEnabledCheckCreatedAt"\`` to `CheckStatsResponse` (`:48`), computed in `foldCheckStatusCounts` (`:167`) as the minimum `OldestCreatedAt` over rows with `Enabled == true`; `nil` when there is none. The stats cache (`:205-251`) needs no change. Update the OpenAPI schema in `server/internal/app/openapi/openapi.yaml` for the stats response.
4. `web/dash0/src/api/hooks.ts:872`: add `oldestEnabledCheckCreatedAt: string | null` to `CheckStats`, and to `EMPTY_CHECK_STATS` as `null`.

### 3. ui-state key `github-star`

1. `server/internal/handlers/uistate/service.go`: extend the allowlist. Add `KeyGitHubStar = "github-star"`, accepted as an exact match and stored under that key as-is (no org resolution). `ResolveKey` (`:89-101`) currently only accepts `onboarding.<org>`; keep that branch unchanged and add the exact-match branch before it. Update the package doc (`:1-16`, "v1: `onboarding.<org>` only"). `MaxValueBytes` already caps the value.
2. Stored value: `{ "shownAt": "<RFC3339>", "outcome": "starred" | "dismissed" }`, `outcome` absent until the user acts.
3. `web/dash0/src/api/hooks.ts` (next to `onboardingUiStateKey`, around `:9120`): add `GITHUB_STAR_UI_STATE_KEY = "github-star"` and a `GitHubStarUiState` type. Reuse `useUiState` / `useSetUiState` (`:9066-9110`).

### 4. Permanent sidebar link

1. New `web/dash0/src/components/layout/github-star-link.tsx`: a `SidebarMenuButton asChild` wrapping `<a href="https://github.com/fclairamb/solidping" target="_blank" rel="noopener noreferrer" data-testid="github-star-sidebar">` with a lucide `Star` icon and the label "Star on GitHub". No star count, no fetch to `api.github.com` (so no CSP change in `dashboardPolicy()`: a navigation is not a fetch). Muted styling, same size as the other footer rows. Renders nothing when `githubStar.enabled === false`.
2. On click: `captureEvent("github_star_link_clicked", { source: "sidebar" })` (`web/dash0/src/lib/analytics.ts:426`, a no-op when analytics is off).
3. Mount it in `AppSidebar.tsx`'s `SidebarFooter` as its own `SidebarMenuItem` between the controls row (`:298-305`) and the user menu (`:306`). Mobile: the footer is inside the sheet sidebar, nothing extra.

### 5. One-time prompt on the dashboard home

1. Pure decision function in new `web/dash0/src/lib/github-star.ts`: `shouldShowGitHubStarPrompt({ enabled, isDemo, stats, uiState, uiStateLoaded, now })` returns true iff all of:
   - `enabled` (public config, missing = true) and not `isDemo`;
   - `uiStateLoaded` (never render before the read completes: rendering first then retracting would flash it, same rule as `onboarding-checklist.tsx:206-210`);
   - `stats.oldestEnabledCheckCreatedAt` is set and at least 7 days before `now`;
   - `stats.down === 0` (no prompt while something is failing);
   - `uiState` has no `outcome`, and either no `shownAt` or `shownAt` less than 14 days before `now`.
2. New `web/dash0/src/components/dashboard/github-star-prompt.tsx`: a small dismissible `Card` (container `data-testid="github-star-prompt"`), using design-reference primitives only. Copy: one sentence of thanks and the ask, no guilt, no mention of any reward. Buttons:
   - "Star on GitHub" (`data-testid="github-star-prompt-star"`): an `<a>` to the repo, new tab; writes `outcome: "starred"`, fires `github_star_prompt_clicked`.
   - "Not now" (`data-testid="github-star-prompt-dismiss"`) and the close icon (`data-testid="github-star-prompt-close"`): write `outcome: "dismissed"`, fire `github_star_prompt_dismissed` with `{ via: "button" | "close" }`.
   - Both hide the card immediately (local state), without waiting for the PUT, like `onboarding-checklist.tsx:167-205`.
   - On first display (no `shownAt`): write `shownAt`, fire `github_star_prompt_shown` once, guarded by a ref like `onboarding-checklist.tsx:153-163`.
3. Mount it in `web/dash0/src/components/dashboard/dashboard-page.tsx` next to `OnboardingChecklist` (`:469`), fed with the `stats` it already has (`:273`). Never render both cards at once: if the onboarding checklist is visible, the star prompt waits for a later visit.
4. Add a fixture of the card to `web/dash0/src/routes/orgs/$org/design-reference.tsx`, near the onboarding checklist section (`:7578-7600`), as AGENTS.md requires for a new pattern.
5. Strings: `common.json` (sidebar label) and `dashboard.json` (prompt) in `web/dash0/src/locales/{en,fr,de,es}/`, real translations (`untranslated-values.test.ts` and `locale-parity.test.ts` enforce it). "GitHub" stays as is.

### 6. Event names

Snake case, verb last, camelCase properties, like the existing dash0 event `quick_check_created` (`web/dash0/src/components/dashboard/empty-state-onboarding.tsx:156`) and the server's `check_created` / `org_created` (`server/internal/analytics/analytics.go:51-60`):

| Event | When | Properties |
|---|---|---|
| `github_star_link_clicked` | sidebar link clicked | `source: "sidebar"` |
| `github_star_prompt_shown` | prompt displayed the first time for this user | `checkAgeDays` (integer, from `oldestEnabledCheckCreatedAt`) |
| `github_star_prompt_clicked` | "Star on GitHub" in the prompt | none |
| `github_star_prompt_dismissed` | "Not now" or close | `via: "button" \| "close"` |

Browser-side only, through `captureEvent`. The server's closed event set in `analytics.go` is not touched.

### 7. Server startup line

`server/internal/app/server.go`, in `Start` right after the "Starting SolidPing node" log (`:3876-3880`): when `s.config.App.StarPrompt`, log once at INFO: `slog.InfoContext(ctx, "If SolidPing is useful to you, a star helps: https://github.com/fclairamb/solidping")`. Once per process start, never repeated. Only the `serve` path: `solidping mcp` and `solidping client` (`server/main.go:101-130`) must not print it.

### 8. CLI

1. `server/pkg/cli/auth.go`: after "Login successful!" in both login paths (`:95-100` and `:182-192`), in text mode only (`cliCtx.IsText()`, never in `--json` / `--output json`), print one line: `If SolidPing is useful to you, a star helps: https://github.com/fclairamb/solidping`. Login is the CLI's first-run moment.
2. `server/cmd/sp/main.go:16-27` and the root command in `server/main.go:83-90`: add the same sentence as the last line of the command's `Description`, so it appears in `sp --help` / `solidping --help`.
3. The CLI does not load the server config: both read `SP_APP_STARPROMPT` from the environment directly (a small helper in `server/pkg/cli`, parsing it like koanf does: `false`/`0` off, anything else or unset on). Help text is static in urfave/cli, so build the `Description` string through that helper at command construction.

### Out of scope

- **Any reward for starring**: no feature unlock, credit, trial extension, badge, gift or "thank you" perk, and no check that the user actually starred. GitHub's Acceptable Use Policies forbid incentivized or inauthentic stars; the copy must not hint at one either.
- Showing a star count, calling the GitHub API, OAuth "star for me" buttons.
- Email, status pages (`web/status0`) and the marketing site.

## Tests

- `server/internal/handlers/uistate/service_test.go`: `github-star` is accepted and round-trips (Set, Get, Delete); `github-star.x`, `github-starX` and `GitHub-Star` are rejected with `ErrInvalidKey`; `onboarding.<org>` still works.
- `server/internal/handlers/checks/stats_test.go` (or the existing fold test): `foldCheckStatusCounts` returns the minimum `OldestCreatedAt` over enabled rows only; a disabled check older than every enabled one is ignored; no enabled rows gives `nil`.
- DB tests for `GetCheckStatusCounts` on SQLite (`make test`) and Postgres (`make test-postgres`): `oldest_created_at` scans into a `time.Time` on both (SQLite stores times as text).
- `server/internal/handlers/publicconfig` test: `githubStar.enabled` is true by default, false with `SP_APP_STARPROMPT=false`.
- `server/internal/config` tests: default `App.StarPrompt == true`; `SP_APP_STARPROMPT=false` sets it false; the env-var recognition tests pass.
- `server/internal/app`: the startup line is logged exactly once with the default config and absent with the option off (capture slog with a test handler).
- `server/pkg/cli`: login text output contains the line; `--json` output does not; `SP_APP_STARPROMPT=false` removes it from login output and from `Description`.
- `web/dash0/src/lib/github-star.test.ts`: `shouldShowGitHubStarPrompt` for: check exactly 7 days old (true) and 6 days 23 h (false); `oldestEnabledCheckCreatedAt: null` (false); `down > 0` (false); demo (false); config disabled (false); config key missing (true); ui-state not loaded (false); `outcome` set to either value (false); `shownAt` 13 days ago (true) and 15 days ago (false).
- Playwright `web/dash0/e2e/github-star.spec.ts` (selectors on `data-testid` only, test-mode login `test@test.com` / `test` / org `test`):
  - The sidebar link `github-star-sidebar` is visible, has `href="https://github.com/fclairamb/solidping"` and `target="_blank"`.
  - Prompt eligible: stub `**/api/v1/orgs/test/checks/stats` with `page.route` (pattern of `account-notifications-discord.spec.ts:78-105`) so `oldestEnabledCheckCreatedAt` is 8 days ago and `down: 0`; `github-star-prompt` is visible on the dashboard home. Click `github-star-prompt-dismiss`: the card disappears; reload: still absent; clear the browser context's storage and log in again: still absent (proves server-side persistence).
  - Same with `github-star-prompt-star`: a popup opens on the repo URL (`context.waitForEvent("page")`, no need to load GitHub), and the card never comes back.
  - Not eligible: stats stubbed with a check 2 days old, or with `down: 1`: the prompt is absent.
  - Option off: stub `**/api/v1/config` with `githubStar: { enabled: false }`: neither `github-star-sidebar` nor `github-star-prompt` exists, even with eligible stats.
  - Reset the user's `github-star` entry (`DELETE /api/v1/me/ui-state/github-star`) in `beforeEach` so tests do not depend on order.
- `make lint`, `make test`, `make test-dash` green.

## Acceptance criteria

- Every dash0 page shows a discreet "Star on GitHub" link in the sidebar footer, with no star count, on SaaS and self-hosted alike.
- A user sees the prompt only on the dashboard home, only when the org has had an enabled check for 7 days or more, nothing is down, and the session is not the demo. Never at signup, never on an empty org.
- After "Star on GitHub", "Not now" or close, that user never sees the prompt again, on any device or org. Ignored prompts stop after 14 days.
- `SP_APP_STARPROMPT=false` removes the link, the prompt, the startup log line and the CLI lines; unset means on.
- The server logs the star line once per start at INFO; `sp auth login` prints it in text mode only; both `--help` outputs end with it.
- The four PostHog events fire with the names and properties above, and nothing fires when analytics is off.
- No reward, no hint of one, anywhere.

## To verify

- How `bun` scans `MIN(created_at)` on SQLite (text column) into `*time.Time`; if it does not parse, scan into a string and parse with the format the SQLite backend already uses for `created_at`.
- That `GET /checks/stats` is fetched on the dashboard home for every member role (viewer included); if not, gate the prompt on the same condition and say so.
- That the root commands' `Description` is printed by urfave/cli v3's default help template (otherwise use `UsageText` or a custom `CustomRootCommandHelpTemplate`).
