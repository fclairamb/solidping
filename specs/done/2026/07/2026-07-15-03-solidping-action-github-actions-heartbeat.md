---
model: sonnet
effort: medium
---

# `solidping-action`: a GitHub Action that heartbeats scheduled workflows

> **Scope warning — most of this ships in a NEW, EXTERNAL repository**
> (`fclairamb/solidping-action`), not in `solidping`. The only deliverable
> inside this repo is the docs page. See "Repository layout" before starting;
> do not attempt to build the action under `solidping/`.

## Problem

**The gap is silence, not failure.** GitHub already emails you when a workflow
fails, so an action that forwards failure notifications is redundant. What
GitHub cannot tell you is that a scheduled job **never ran at all**:

- GitHub **auto-disables scheduled workflows after 60 days of repository
  inactivity** — silently. The nightly backup simply stops, with no failure, no
  email, no signal.
- A broken cron expression, a renamed default branch, or a deleted secret can
  all stop a schedule from firing. Each produces zero notifications.

You find out weeks later. This is exactly the failure mode SolidPing's
heartbeat check exists for: `period` + `grace` means "if no ping arrives in
time, open an incident" — an assertion about *absence*, which no
notify-on-failure system can make.

**Nothing about this requires server work.** The endpoint already accepts
everything needed today: `POST|GET /api/v1/heartbeat/:org/:identifier` with
`?token=`, `?status=running|up|down|error`, and an optional `{"message": "..."}`
body (`heartbeat/handler.go:35-64`), and it already records caller
User-Agent / IP / method. A user can wire this up with `curl` right now.

So the honest question is what an action adds over a documented `curl` line.
Three things:

1. **Correct status mapping.** Translating `job.status` into SolidPing's
   `up`/`down`/`error` vocabulary, including the non-obvious call that a
   *cancelled* job must not report `down` (cancelling a job is not an outage,
   and paging someone for it trains them to ignore the channel).
2. **An actionable message.** Auto-building the run URL, workflow name, run
   number, commit SHA, and actor from the `github` context, so the Slack alert
   is one click from the failed run. Hand-rolling this in every workflow is the
   boilerplate people get wrong or skip.
3. **Marketplace distribution.** This is the honest primary driver. A listing
   on the GitHub Actions Marketplace is a free acquisition channel where people
   already browse for monitoring tooling. The engineering could be a docs
   snippet; the *distribution* cannot.

## Proposal

### Fit: scheduled workflows only — say so out loud

The README and docs must state plainly that this is for `on: schedule`
workflows.

Heartbeat asserts "a ping is expected every N minutes". That is meaningful for a
cron job and **meaningless for push-triggered CI**: a push-triggered job has no
period, so if nobody pushes for three days the heartbeat goes down and pages
someone about a repo that is merely quiet. The only way to suppress that is an
infinite grace, at which point the liveness check provides no liveness — you've
built an event reporter wearing a check's clothes.

Reporting push-triggered CI failures is a genuinely useful, genuinely
*different* feature (a "job failed" event, not a liveness assertion). It must
not be smuggled in through this door. Out of scope here.

### Implementation: composite action, bash + curl

A **composite** action (`runs.using: composite`, `shell: bash`). No Node, no
`ncc` bundle, no committed `dist/` to drift out of sync, auditable at a glance,
and `curl` + bash are present on all GitHub-hosted runners including Windows.

Usage — one step, explicit:

```yaml
- uses: fclairamb/solidping-action@v1
  if: always()
  with:
    org: acme
    check: nightly-backup
    token: ${{ secrets.SOLIDPING_HEARTBEAT_TOKEN }}
    status: ${{ job.status }}
```

Inputs:

| Input | Required | Default | Notes |
|---|---|---|---|
| `server` | no | `https://solidping.io` | self-hosted override |
| `org` | yes | — | org slug |
| `check` | yes | — | heartbeat check identifier (slug or uid) |
| `token` | yes | — | per-check heartbeat token |
| `status` | yes | — | pass `${{ job.status }}` |
| `message` | no | auto | overrides the auto-built message |

Status mapping (`job.status` → SolidPing):

- `success` → `up`
- `failure` → `down`
- `cancelled` → **no ping at all**, exit 0 (see rationale above)
- anything unrecognised → `error`

The action must **never fail the job it monitors**: if the ping itself errors
(network, 5xx, bad token), log a clear warning via `::warning::` and exit 0. A
monitoring tool that breaks the build it watches gets removed within the week.
The one exception worth considering is a 401, where failing loudly on
first setup is arguably kinder than silently never reporting — flag this
tradeoff in the PR rather than deciding it here.

### Deliberately NOT a post-hook wrapper

The tempting design is one `uses:` at the top of the job with a `post:` hook
firing automatically at the end, so users never write `if: always()`. Rejected:

- Composite actions do not support `pre`/`post` hooks, so this forces a JS
  action — Node, a build step, and a committed bundle.
- Post steps are not handed `job.status`. Getting it means querying the Actions
  API (`GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs`) for your own
  job's conclusion, which needs a `GITHUB_TOKEN` input and extra permissions.

That is a large amount of machinery to save one idiomatic, widely-understood
`if: always()`. Revisit only if users actually ask.

### Repository layout

New repo **`fclairamb/solidping-action`** — singular. One repo, one action,
`action.yml` at the root. Reasons this cannot live in `solidping/`:

1. **The Marketplace requires `action.yml` at the repository root.** It cannot
   be published from a subdirectory. This alone is decisive.
2. **Tag namespace.** Actions are consumed via a floating `@v1` tag. `solidping`
   uses release-please and is at `v0.4.0`; sharing a tag namespace between a
   server release train and an action's floating major tag is a mess.
3. **Checkout size.** Every job using the action clones its repo. Cloning the
   solidping monorepo to run one `curl` is absurd.

Name it singular: `solidping-actions` implies a suite that does not exist, and
only a root action is publishable anyway.

Contents: `action.yml`, `README.md`, `LICENSE`, a `.github/workflows/test.yml`
that exercises the action against a real heartbeat check, and a release workflow
that moves the floating `v1` tag on each `vX.Y.Z` release.

### README

Lead with the failure mode, not the feature list. The opening line is
approximately *"Your nightly workflow stopped running two months ago and nobody
noticed."* Explain GitHub's 60-day auto-disable explicitly — it is the single
most compelling, most under-known justification for the whole tool. Then the
copy-paste snippet, then inputs, then the scheduled-only scope note.

## Out of scope

- **Push-triggered CI monitoring.** Wrong primitive; see above.
- **Auto-creating the check from CI.** It needs a full API token instead of the
  per-check heartbeat token, putting a far more powerful secret in every repo to
  save clicking "new check" once. Bad trade.
- **A `status=running` ping at job start.** The endpoint supports it, but with
  the single-step `if: always()` design there is no start hook to fire it from,
  and it would double the step count for marginal value. Reconsider only
  alongside a post-hook design.
- **GitLab CI / Jenkins / CircleCI equivalents.** Prove the pattern on one
  platform first.
- **Bundling the `sp` CLI.** Installing a binary to send one HTTP request is
  strictly worse than `curl` here.

## Dependencies

Spec `2026-07-15-02-heartbeat-header-auth-and-structured-body` is a **soft**
dependency — it does not block this work:

- Header auth lets the action send the token in `Authorization: Bearer` rather
  than a query string (GitHub masks secrets in logs, but the token still reaches
  proxy access logs on the SolidPing side).
- A structured body lets the action send `{runUrl, workflow, sha, actor}` as
  real fields instead of cramming them into one prose string.

Ship the action against today's query-param + `message` API if 02 has not
landed; adopt both when it does (a `v1.1` of the action, not a breaking change).

## Acceptance criteria

- `fclairamb/solidping-action` exists with `action.yml` at the root, published
  to the GitHub Actions Marketplace, consumable as `@v1`.
- A scheduled workflow using the snippet above reports `up` on success and
  `down` on failure to a real heartbeat check; a cancelled run sends nothing.
- The auto-built message contains a clickable run URL, the workflow name, run
  number, short SHA, and actor.
- A ping failure (network error, 5xx, invalid token) emits a `::warning::` and
  leaves the job's own conclusion untouched.
- The action works against a self-hosted server via `server:`.
- The repo's own `test.yml` workflow exercises the action end to end and passes.
- README leads with the silent-stall failure mode and names GitHub's 60-day
  auto-disable; the scheduled-only scope is stated explicitly.
- **In this repo:** the Heartbeat section of
  `web/docs/docs/features/check-types.md` (~line 663) gains a "Monitoring
  GitHub Actions" subsection with the snippet and a link to the action, framed
  around the same silent-stall pitch.

## Implementation plan

**External repo (`fclairamb/solidping-action`):**

- [ ] Scaffold the repo: `action.yml` (composite), `README.md`, `LICENSE`.
- [ ] Implement the ping step in bash: resolve inputs, map `job.status`, build
      the message from the `github` context, `curl` the heartbeat endpoint,
      never fail the parent job.
- [ ] `.github/workflows/test.yml`: end-to-end run against a real check
      (token from repo secrets), covering success and failure paths.
- [ ] Release workflow: on `vX.Y.Z`, move the floating `v1` tag.
- [ ] Publish to the GitHub Actions Marketplace.

**This repo (`solidping`):**

- [ ] `web/docs/docs/features/check-types.md`: add "Monitoring GitHub Actions"
      under Heartbeat — the pitch, the snippet, the scheduled-only note, and a
      link to the action.

## Implementation Plan

This session implements **only** the in-repo docs deliverable listed under
"This repo (`solidping`)" above. Everything under "External repo
(`fclairamb/solidping-action`)" — scaffolding `action.yml`, the composite
bash implementation, `test.yml`, the release workflow, and publishing to the
GitHub Actions Marketplace — is out of reach for this session: it requires
creating and pushing to a brand-new external GitHub repository, which this
working tree cannot do. It is explicitly not attempted here, is not stubbed
inside `solidping/`, and remains open work for whoever has access to create
`fclairamb/solidping-action`.

In-scope work: add a "Monitoring GitHub Actions" subsection to the Heartbeat
section of `web/docs/docs/features/check-types.md`, matching the spec's
Proposal snippet, status mapping, scheduled-only scope note, and a link to
`https://github.com/fclairamb/solidping-action`. Since spec
`2026-07-15-02-heartbeat-header-auth-and-structured-body` has already landed
(archived to `specs/done/2026/07/`), the docs example uses the
`Authorization: Bearer` header form rather than the `?token=` query param.
