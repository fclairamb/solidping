---
model: sonnet
effort: medium
---

# In-app bug report must be off by default, with no repo baked into the code

## Problem
SolidPing already has the in-app report (widget in `web/dash0/src/components/feedback/`, anonymous `POST /api/mgmt/report` in `server/internal/handlers/feedback/`, GitHub issue filing in `service.go:250-295`). It is switched on by `ComputeBugReportEnabled` (`server/internal/config/config.go:3120`): token and repo both set, and `features/handler.go:59` exposes it to the UI.

The default is not "off" in practice: `config.go:1880-1882` hard-codes `App.GitHub.Repo = "fclairamb/solidping"`. Any deployment that sets only `SP_APP_GITHUB_ISSUES_TOKEN` (or the bare `GITHUB_ISSUES_TOKEN`, `config.go:2126-2135`) starts filing issues in the public repo. The repo name must not live in code.

Wanted:
- Disabled by default: no token and no repo in the defaults, so a fresh install shows no report button and `/api/mgmt/report` files nothing.
- Enabled only on `solidping.k8xp.com` (dev) and `solidping.io` (prod), configured through env, pointing at `fclairamb/solidping-business`.

## Proposal
1. `server/internal/config/config.go:1880-1882`: drop the default `Repo`, leave `AppGitHubConfig{}` empty. Keep `DefaultFeedbackMaxStorageBytes`.
2. Check the whole path is inert when disabled (see To verify): the features payload (`handlers/features/handler.go:59`), the dashboard widget gating (`FeedbackButton.tsx`), and `feedback/service.go:184`.
3. Update the docs that state the old default (grep `fclairamb/solidping` and `SP_APP_GITHUB_REPO` under `docs/`, `wiki/`, `server/CLAUDE.md`, deploy examples in `deploy/`). Say: off unless `SP_APP_GITHUB_ISSUES_TOKEN` and `SP_APP_GITHUB_REPO` are both set.
4. Deployment, in `~/code/fclairamb/k8xp/k8s/solidping/overlays/dev` and `overlays/prod`: set `SP_APP_GITHUB_REPO=fclairamb/solidping-business` as plain env, and add `SP_APP_GITHUB_ISSUES_TOKEN` to each overlay's sealed secret (fine-grained PAT, issues:write on `solidping-business` only, stored in gopass, never in the repo). Do not touch the `*-checks-*` worker overlays (they serve no UI). Follow the k8xp sealed-secret procedure, and never `kubectl apply -k` blindly.
5. Manual check on `solidping.k8xp.com`: submit a report, confirm an issue lands in `fclairamb/solidping-business` with the `in-app-report` label.

## Tests
- `server/internal/config/config_test.go`: `Load` with no env gives `App.GitHub.Repo == ""` and `EnableBugReport == false`.
- Same file: token set but repo unset stays disabled (this is the case the old default broke); repo set but token unset stays disabled; both set (`SP_APP_GITHUB_REPO=fclairamb/solidping-business`) enables.
- `server/internal/handlers/features`: `bug_report` is `false` on the default config, `true` when both are set.
- `server/internal/handlers/feedback/service_test.go`: with the default config no GitHub call is made (fake dispatcher never invoked), and the report is still stored or refused as today.
- Web e2e in `web/dash0/e2e/` (mock `/features` with `bug_report: false`): the feedback button is not rendered. Add the `true` case if not already covered.

## To verify
- Whether `FeedbackButton.tsx` (and any keyboard shortcut or command-menu entry) is gated on the features flag, or always rendered.
- Whether `POST /api/mgmt/report` still accepts and stores screenshots (quota, `files` storage) when disabled. If so, it should answer 404 or 403 instead of taking anonymous uploads for nothing.
- Which sealed-secret file(s) hold the SolidPing env in the k8xp overlays, and whether dev and prod share one.

## Open questions
- Should `/api/mgmt/report` return an error when the feature is off, rather than storing the report? Recommended: yes, 404, since the button is hidden anyway and the endpoint is anonymous.

## Resolved open questions
- `/api/mgmt/report` must return 404 when the bug-report feature is off, and store nothing.
