# Terraform Provider — API Completeness Audit

> In-tree deliverable of spec `specs/done/2026/05/2026-05-25-10-terraform-provider.md`
> (Terraform Provider — Configuration as Code). The provider itself lives in a separate
> repo (`terraform-provider-solidping`); this note records whether the v1 REST API exposes
> everything a declarative (Terraform) lifecycle needs for each of the five resources.

## Method

For each resource the spec maps to a Terraform resource/data source, the audit checks the
four properties a clean declarative lifecycle requires (from the spec's "API completeness
audit" checklist):

1. **Create returns the full object** (incl. server-assigned `uid`) — so the provider can
   set state without a follow-up GET.
2. **GET by `uid`** returns every field Create accepts — no write-only field that can't be
   read back (would cause perpetual diffs). Secret-bearing fields are returned as a
   placeholder key list (`configPrivateKeys` / `settingsPrivateKeys`), never values — the
   provider treats these as `sensitive` / write-only and must not diff on them.
3. **PATCH** accepts partial updates and preserves omitted secret keys.
4. **DELETE** is idempotent enough — `404` on already-deleted is acceptable.

Plus one cross-cutting property the spec calls out (import addressing): `terraform import`
takes `org/uid`, so **GET/PATCH/DELETE must be reachable by `uid`**.

Code references are file:line on the audited branch.

## Per-resource findings

### `solidping_check` → `handlers/checks/` ✅

| Property | Status | Evidence |
|---|---|---|
| Create returns full object | ✅ | `CreateCheck` returns `CheckResponse` with `uid` (`checks/handler.go:232-237`, `service.go:468-509`). |
| GET by uid full read-back | ✅ | Route `GET /checks/:checkUid` resolves uid **or** slug (`server.go:489`, `checks/handler.go:241-267`). |
| Secret fields as placeholders | ✅ | `CheckResponse.ConfigPrivateKeys []string` lists encrypted keys; values stripped (`checks/service.go:481`, `1765-1784`). |
| PATCH partial + preserves secrets | ✅ | `UpdateCheck` PATCH; omitted secret keys preserved from `config_private` (`checks/service.go:999`, credential-encryption merge). |
| DELETE idempotent (404 ok) | ✅ | `DeleteCheck` → 404 `CHECK_NOT_FOUND` when gone (`checks/handler.go:347-356`, `563-575`). |
| Import by `org/uid` | ✅ | `:checkUid` accepts uid. |

Notes for the provider: `config` is a free-form map; secret keys appear in
`configPrivateKeys` and must be modeled as `sensitive`/write-only with a state-preserving
suppressor. `status`, `lastResult`, `lastStatusChange`, `createdAt` are computed/read-only.

### `solidping_channel` → `handlers/channels/` ✅ (with documented Slack caveat)

| Property | Status | Evidence |
|---|---|---|
| Create returns full object | ✅ | `CreateChannel` → `toResponse(conn, true)` with `uid` (`channels/service.go:209-264`). |
| GET by uid full read-back | ✅ | `GET /channels/:uid` → `toResponse(conn, true)` includes settings (`server.go:775`, `service.go:179-207`). |
| Secret fields as placeholders | ✅ | `ChannelResponse.SettingsPrivateKeys []string`; secret keys stripped from `settings` (`channels/service.go:73`, `104-139`). |
| PATCH partial + preserves secrets | ✅ | `UpdateChannel` PATCH-merge; absent secret keys preserved, explicit null clears (`channels/service.go:267-308`). |
| DELETE idempotent (404 ok) | ✅ | `DeleteChannel` → 404 when gone. |
| Import by `org/uid` | ✅ | `:uid` route. |

**Slack caveat (per spec + `2026-05-25-04-fix-slack-channel-install-only`).** `POST /channels`
with `type=slack` is rejected (`ErrSlackManualCreate`, `channels/service.go:238-240`). Slack
channels originate only from the OAuth install flow and are **not manageable via Terraform**.
`solidping_channel` covers webhook/email/discord/googlechat/mattermost/ntfy/pagerduty/
pushover/freebox only; document the Slack exclusion in the resource docs.

Minor (non-blocking): **List** (`GET /channels`) returns channels without `settings`
(`toResponse(conn, false)`, `service.go:173`) — only **Get-by-uid** includes settings. The
provider reads back via Get-by-uid, so this does not cause diffs; relevant only if a data
source enumerates via List then expects settings (it should Get each by uid).

### `solidping_escalation_policy` → `handlers/escalationpolicies/` ⚠️ (uid addressing gap)

| Property | Status | Evidence |
|---|---|---|
| Create returns full object | ✅ | `CreatePolicy` handler re-fetches and returns full `policyJSON` incl. `uid` + expanded `steps`/`targets` (`escalationpolicies/handler.go:204-222`, `69-79`). |
| GET full read-back | ✅ (by slug) | `GET /:slug` returns full detail (`handler.go:226-238`). |
| PATCH partial | ✅ (by slug) | `UpdatePolicy`; `steps` replace wholesale when present (`service.go:133-145`). |
| DELETE idempotent (404 ok) | ✅ (by slug) | `DeletePolicy` → 404 when gone. |
| **Import by `org/uid`** | ❌ | **Only slug-addressable.** Routes are `/:slug`; handlers call `GetPolicyBySlug` (`server.go:659-661`, `handler.go:233`). No by-uid route, service method, or DB method exists (`service.go` has only `GetPolicyBySlug`; no `GetEscalationPolicyByUID` in `db/`). `terraform import org/uid` cannot fetch the resource. |

No secret fields. Gap → follow-up task `2026-05-25-14`.

### `solidping_oncall_schedule` → `handlers/oncallschedules/` ⚠️ (uid addressing gap)

| Property | Status | Evidence |
|---|---|---|
| Create returns full object | ✅ | `CreateSchedule` handler returns `scheduleResponse` incl. `uid` + `userUids` roster (`oncallschedules/handler.go:219-221`, `40-90`). |
| GET full read-back | ✅ (by slug) | `GET /:slug` returns schedule + roster (`handler.go:227-244`). |
| PATCH partial | ✅ (by slug) | `UpdateSchedule`; `userUids` replaced when present (`service.go:155-166`). |
| DELETE idempotent (404 ok) | ✅ (by slug) | `DeleteSchedule` → 404 when gone. |
| **Import by `org/uid`** | ❌ | **Only slug-addressable at the route layer.** Routes are `/:slug`; `GetSchedule` calls `GetScheduleBySlug` (`server.go:628-630`, `handler.go:234`). A `GetScheduleByUID` **service** method exists (`service.go:104-118`) but is **not wired to any route**, so `terraform import org/uid` cannot fetch the resource over HTTP. |

`currentlyOnCall` and `icalEnabled` are computed/read-only (provider marks computed).
Gap → follow-up task `2026-05-25-14`.

### `solidping_status_page` → `handlers/statuspages/` ✅

| Property | Status | Evidence |
|---|---|---|
| Create returns full object | ✅ | `CreateStatusPage` → `StatusPageResponse` with `uid` (`statuspages/service.go:83-98`, create path). |
| GET by uid full read-back | ✅ | `GET /:statusPageUid` resolves uid **or** slug (`GetStatusPageByUidOrSlug`, `service.go:309-318`, `server.go:813`). |
| PATCH partial | ✅ | `UpdateStatusPage` PATCH (`service.go:168-179`). |
| DELETE idempotent (404 ok) | ✅ | `DeleteStatusPage` → 404 when gone. |
| Import by `org/uid` | ✅ | `:statusPageUid` accepts uid. |

No secret fields. Minor (non-blocking): `enabled` is settable on PATCH but not on Create
(`CreateStatusPageRequest` has no `enabled`; `UpdateStatusPageRequest` does, `service.go:155-179`)
— the provider models `enabled` as `Optional + Computed` (defaults true on create) and only
PATCHes it on update. Sections/resources are nested sub-resources under their own endpoints
(`/status-pages/:uid/sections/...`) — a v1 `solidping_status_page` resource manages the page
header; section/resource management is a future nested-resource concern, not a read-back gap.

## Cross-cutting (all resources) ✅

- **Auth** — org-scoped PAT via `POST /api/v1/orgs/:org/tokens` (`server.go:374-376`,
  `authHandler.CreateToken`); listable/revocable (`server.go:355,359`). No new auth work.
- **Envelope** — list responses wrap in `{ "data": [...] }`.
- **Error shape** — `{ title, code, detail }`; provider maps `404` on read → "resource gone"
  (recreate), `VALIDATION_ERROR` → plan-time error.
- **PATCH semantics** — partial updates everywhere; secret keys absent from the request
  preserve the encrypted value (checks, channels).

## Gaps and follow-ups

| # | Gap | Severity | Follow-up |
|---|---|---|---|
| 1 | `escalation-policies` and `on-call-schedules` are only addressable by **slug**, not **uid**; `terraform import org/uid` cannot fetch them. | Blocks clean `import org/uid` for 2 of 5 resources. | `specs/todos/2026-05-25-14-uid-addressing-escalation-oncall.md` |
| 2 | `escalation-policies` and `on-call-schedules` are missing from `wiki/api-specification/`. | Docs only; non-blocking. | Folded into task `14` (doc the endpoints while adding uid routes). |

Everything else required for a clean declarative lifecycle (create-returns-full-object,
read-back fidelity, secret placeholders, PATCH preserve-secrets, idempotent delete) is
**present today** for all five resources. The provider repo is therefore unblocked: it can
ship `solidping_check`, `solidping_channel`, `solidping_status_page` with `import org/uid`
immediately, and `solidping_escalation_policy` / `solidping_oncall_schedule` either by
importing on **slug** in the interim or after follow-up task `14` lands uid addressing.
