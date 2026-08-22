# Event catalogue (audit trail)

Every audit fact SolidPing records lives in the **one** org-scoped `events`
table and is read through
[`GET /api/v1/orgs/:org/events`](results-incidents.md#get-apiv1orgsorgevents).
There is deliberately no second audit store: one table, one retention policy,
one UI, one query language.

Events are **append-only**. The single exception is `auth.login_failed`
folding, described below.

## Actor and provenance

| Column / field | Meaning |
|---|---|
| `actorType` | `system` \| `user` \| `api_token` \| `service` |
| `actorUid` (`actorUserUid` as a filter) | The acting user. NULL for system-originated events. |
| `sourceIp` | Client address, bare (no port). NULL when unknown or when `audit.capture_ip` is off. Admin/owner-only in API responses. |
| `userAgent` | Raw `User-Agent`, truncated. Admin/owner-only. |

Emission happens in the **service layer**, never in HTTP middleware, so an
event carries domain meaning ("this policy's name and repeat window changed")
rather than transport trivia ("a PATCH hit this URL"), and internal callers
that never went through HTTP are covered too. Middleware's only job is to
capture the actor and the request provenance onto the context.

## Redaction rules

These are contract, not implementation detail:

- **No secrets, ever.** Password material, token values, API keys, bot tokens,
  webhook URLs, signing secrets and credential blobs are stripped by key name
  (recursively, and case-insensitively) before a payload is written.
- **Update events record changed FIELD NAMES** in `changed_fields`, plus safe
  old→new values in `changes` for non-sensitive **scalar** fields only. A
  sensitive field appears in `changed_fields` and never in `changes` — "the
  webhook secret was rotated" is an audit fact; its value is not.
- **Token events store the token's name and prefix**, never its value.
- **`config.applied` stores summary counts**, never the manifest body.
- Strings are truncated and nesting is bounded, so a config payload cannot be
  smuggled in under an innocuous key name.

## Common payload keys

| Key | Meaning |
|---|---|
| `target_type` / `target_uid` / `target_name` | What the event is about |
| `changed_fields` | Sorted names of the fields an update touched |
| `changes` | `{field: {from, to}}` for the non-sensitive scalar subset |

## Families

### `auth.*` — admin/owner-only

| Type | Payload highlights |
|---|---|
| `auth.login_succeeded` | `auth_method`, `email`, `role` |
| `auth.login_failed` | `email`, `reason`, `count`, `first_at`, `last_at` |
| `auth.logout` | — |
| `auth.token_created` | `token_kind` (`pat` / `agent_enrollment`), `token_name`, `token_prefix` |
| `auth.token_revoked` | `token_kind`, `token_name` |

**`auth_method` values.** Every path that mints a session records one, because
a session IS a `refresh`-type `user_tokens` row and they all go through
`auth.Service.startSession` (enforced structurally by
`TestEverySessionMintingPathGoesThroughStartSession`):

| Value | Path |
|---|---|
| `password`, `ldap`, `passkey` | local first factors |
| `oidc`, `saml`, `github`, `gitlab`, `google`, `microsoft`, `discord`, `slack` | federated connectors, named via `auth.WithLoginMethod` |
| `oauth` | fallback for a connector that did not name itself |
| `<first>+totp`, `<first>+recovery_code` | 2FA-completed logins — the first factor survives the hand-off on the temp token, so a TOTP sign-in reads `password+totp`, not just `totp` |
| `invitation`, `registration` | sessions minted by accepting an invite / confirming a registration |
| `switch_org`, `org_session` | sessions minted for another org the user already belongs to, or re-minted because the org itself changed (creation, slug rename) |

A federated login that is NOT admitted to the org (the membership-request
outcome) mints no org-scoped session, and therefore records no login — claiming
one would assert access that was never granted.

**`auth.login_failed` flood control.** This is the one event an
unauthenticated stranger can cause at will, so a naive one-row-per-attempt
implementation would let a credential-stuffing run destroy the very trail
meant to record it. Two independent brakes:

1. **Folding** — repeats of the same `(org, email, source IP)` inside
   `audit.failed_login_fold_window_minutes` (default 10) update a `count` on
   the row already written instead of adding another. One row then reads
   "this address tried this account 47 times between 09:02 and 09:11".
2. **A per-org hourly ceiling** on rows *created*
   (`audit.failed_login_max_per_org_per_hour`, default 60). An attempt that
   rotates the email or IP every time defeats folding by design; the ceiling
   bounds it anyway. Folding stays free once a row exists.

Both live in process memory, so a multi-replica deployment enforces the
ceiling per replica.

An attempt that cannot be resolved to an organization is **not** recorded —
the table is org-scoped and there is no global bucket for a stranger to write
into.

### `member.*`

`member.invited`, `member.joined`, `member.removed`, `member.role_changed`.
Payloads carry `email`, `role`, and for role changes `previous_role`.
`member.role_changed` fires only when the role actually moved.

`member.joined` also covers the memberships a LOGIN creates on the fly, which
is how most seats appear in an SSO org. Its `source` says which admission rule
let the user in: `admin_add`, `invitation`, `org_bootstrap`,
`invitation_at_login`, `slack_workspace`, `email_pattern`, `auto_join_pattern`.

### Configuration families

`integration.*`, `escalation_policy.*`, `oncall_schedule.*`, `status_page.*`,
`maintenance_window.*` — `created` / `updated` / `deleted` each, plus
`org.settings_updated` and `config.applied`.

- `integration.updated` carries `settings_keys_touched` (top-level key **names**
  only) alongside `changed_fields`.
- `maintenance_window.updated` also covers the check-attachment set, with
  `check_count` / `check_group_count`.
- `config.applied` carries `manifest`, `created`, `updated`, `deleted`,
  `unmanaged`, `pruned`, `forced`, `errors`.

### Pre-existing families

`check.*`, `incident.*`, `statuspage.incident.*`,
`statuspage.subscriber.disabled`, `status_update.*`, `org.activation.*` predate
the audit work and are unchanged, except that `check.*` events now carry a real
actor (before, they hardcoded `actor_type=user` and never set `actor_uid`, so
"who deleted this check?" had no answer).

## Filters

`GET /api/v1/orgs/:org/events` accepts `eventType` (exact), `type` (family
prefix), `actorUserUid`, `targetType`, `targetUid`, `sourceIp`, `since`,
`until`, `cursor` and `limit`.

Two of them are role-sensitive, and both fail **closed and quietly**:

- the `auth` family is excluded for a non-admin, whether they filtered for it
  or not;
- `sourceIp` is *ignored* for a non-admin rather than rejected. Withholding the
  column while honouring the predicate would leave the fact just as reachable
  (ask for an address, get a non-empty page), and erroring on it would turn the
  endpoint into an oracle for "am I an admin?".

## Retention

The `events_cleanup` job runs daily and removes events older than
`audit.retention_days` (default **365**), in batches, with a per-run ceiling so
a first sweep on a long-lived installation cannot monopolize a job worker.

Resolution order, at each run: `SP_AUDIT_RETENTION_DAYS` → the global
`audit.retention_days` DB parameter → the koanf value → 365. A value of `0` or
less means **keep forever** and disables the sweep — a supported choice for an
operator under a legal hold.

## Configuration

| Key | Env | Default | Effect |
|---|---|---|---|
| `audit.capture_ip` | `SP_AUDIT_CAPTURE_IP` | `true` | When false, `source_ip` is never stored. The user agent is unaffected. |
| `audit.retention_days` | `SP_AUDIT_RETENTION_DAYS` | `365` | Retention window; `<= 0` disables the sweep. |
| `audit.failed_login_fold_window_minutes` | `SP_AUDIT_FAILED_LOGIN_FOLD_WINDOW_MINUTES` | `10` | Fold window. |
| `audit.failed_login_max_per_org_per_hour` | `SP_AUDIT_FAILED_LOGIN_MAX_PER_ORG_PER_HOUR` | `60` | Per-org hourly ceiling on created failed-login events. |

Every key is snake_case and therefore unreachable by koanf's env loader; they
are bound by hand in `applyAuditEnv` and listed in `manualReaderEnvVars`.

## Not in scope

Streaming export (SIEM webhook / syslog) is deliberately **not** built. The
in-product trail comes first; a push path is a separate spec.
