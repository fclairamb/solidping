# Email Passive Checks

## Goal

Add an `email` check type for **passive** monitoring driven by incoming email. Each check has a unique address; an email arriving at that address is recorded as a check result, exactly the way an HTTP heartbeat ping is recorded for a `heartbeat` check.

This builds directly on `2026-04-29-01-email-inbox-jmap.md` — the JMAP inbox manager already delivers each new email to registered handlers. Here we implement the handler that turns an email into a `models.Result` for the right check, and we teach the worker to handle this check type passively (same way it handles `heartbeat`).

## Why mirror heartbeat?

Heartbeat already implements every concept we need:

- A token stored in `check.config` authenticates incoming pings.
- The worker doesn't actively probe — it queries the latest result and decides UP/DOWN based on recency vs. period.
- The check's `period` is repurposed as "expected interval between pings".
- Optional `?status=` lets the sender report explicit failure (`down` / `error`) instead of always reporting success.

Email is the same shape, with a different transport. Reusing the heartbeat semantics keeps the worker, incidents, status pages, and notifications totally unchanged.

## Address format

Every email check has a randomly generated token (24 hex chars). The full address is:

```
<token>@<addressDomain>
```

`addressDomain` comes from the `email_inbox` system parameter (spec 01). Example: `4f9c2a1e8b7d6e5c4a3b2c1d@ingest.solidping.example`.

Two address-level shortcuts mirror the `?status=` query parameter on heartbeat:

| Format | Resulting status |
|--------|------------------|
| `<token>@domain` | `up` (default) |
| `<token>+down@domain` | `down` |
| `<token>+error@domain` | `error` |
| `<token>+up@domain` | `up` |
| `<token>+running@domain` | `running` |

Plus-addressing is widely supported (Gmail, Outlook, Fastmail, postfix) and lets senders signal failure without crafting subjects.

If plus-addressing isn't usable, the same statuses can be supplied via either:

- **Subject tag** — `[DOWN]`, `[UP]`, `[ERROR]`, `[RUNNING]` anywhere in the subject (case-insensitive).
- **Header** — `X-SolidPing-Status: down` (case-insensitive value).

Resolution priority: plus-addressing > header > subject tag > default `up`.

---

## 1. Check type registration

`server/internal/checkers/checkerdef/types.go`:

- Add `CheckTypeEmail CheckType = "email"`.
- Add to `checkTypesRegistry` with labels `["safe", "standalone", "category:other"]` and description `"Receive status updates via incoming email"`.
- Add to `ListCheckTypes()`.

## 2. `checkemail` package

`server/internal/checkers/checkemail/` (mirror layout of `checkheartbeat`):

### `config.go`

```go
type EmailConfig struct {
    Token string `json:"token,omitempty"`
}
```

`FromMap` / `GetConfig` round-trip `token` like `HeartbeatConfig`.

### `checker.go`

```go
type EmailChecker struct{}

func (c *EmailChecker) Type() checkerdef.CheckType { return checkerdef.CheckTypeEmail }

func (c *EmailChecker) Validate(spec *checkerdef.CheckSpec) error {
    // - auto-generate 24-byte hex token if missing
    // - default name "email", default slug "email"
}

func (c *EmailChecker) Execute(_ context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
    return nil, ErrNotExecutable
}
```

`ErrNotExecutable` is a sentinel like the one in `checkheartbeat`.

### Token generation

24 random bytes (`crypto/rand`) → 48 hex chars. Long enough that we don't need org-scoping in the lookup — the token alone identifies the check globally with negligible collision risk.

## 3. Registry

`server/internal/checkers/registry/registry.go` — add `checkemail` import and the two `case` lines (`GetChecker`, `ParseConfig`).

## 4. Worker passive handling

`server/internal/checkworker/worker.go` — generalize the heartbeat short-circuit so it covers both `heartbeat` and `email`:

```go
if isPassive(checkType) {
    return r.executePassiveJob(ctx, logger, checkJob)
}
```

`isPassive` returns true for `CheckTypeHeartbeat` or `CheckTypeEmail`.

`executePassiveJob` already exists as `executeHeartbeatJob`; rename + reuse. The behaviour is identical: query the latest result, save UP if it's within `check.period`, otherwise DOWN, attach descriptive output (`"Email received"` / `"No email received"` / `"Email overdue"`).

## 5. Email ingestion handler

New package `server/internal/handlers/emailcheck/`. This is the JMAP `Handler` that spec 01 plugs into the inbox manager.

### `handler.go`

```go
type Handler struct {
    db          db.Service
    incidentSvc *incidents.Service
}

func NewHandler(dbService db.Service, jobSvc jobsvc.Service) *Handler { /* ... */ }

func (h *Handler) HandleEmail(ctx context.Context, mb *jmap.Mailboxes, email jmap.Email) (jmap.Outcome, error) {
    token, status, ok := extractTokenAndStatus(email)
    if !ok {
        return jmap.OutcomeIgnored, nil
    }

    check, err := h.db.GetCheckByEmailToken(ctx, token)
    if err != nil || check == nil {
        // Token didn't match any active check — reject so the inbox doesn't grow.
        slog.Warn("email-check: unknown token", "token", token, "subject", email.Subject)
        return jmap.OutcomeRejected, nil
    }

    if checkerdef.CheckType(check.Type) != checkerdef.CheckTypeEmail {
        return jmap.OutcomeRejected, fmt.Errorf("token belongs to non-email check %s", check.UID)
    }

    // Save result + run incident processing — same body as heartbeat.Service.ReceiveHeartbeat
    return jmap.OutcomeProcessed, h.recordResult(ctx, check, status, email)
}
```

`extractTokenAndStatus(email)` returns the first token-bearing recipient and the resolved status. Recipient regex matches `^([0-9a-f]{48})(\+(up|down|error|running))?@`. Header / subject overrides apply only when the recipient already matches a token (i.e. we've decided the email is for us).

`recordResult` builds a `models.Result` exactly like `heartbeat.Service.ReceiveHeartbeat`, with extra metadata in `output`:

```json
{
  "message": "Email received",
  "from": "alerts@example.com",
  "subject": "[DOWN] Backup failed: disk full",
  "messageId": "<abc@mail>",
  "receivedAt": "2026-04-29T14:30:00Z"
}
```

After saving, run `incidentSvc.ProcessCheckResult` (skip for `running`, same as heartbeat).

### `db.Service` lookup

Add `GetCheckByEmailToken(ctx, token string) (*models.Check, error)` to `db.Service`. Implementation: `SELECT * FROM checks WHERE type = 'email' AND config->>'token' = ? AND deleted_at IS NULL`. Add a partial index in a new migration:

```sql
CREATE INDEX checks_email_token_idx ON checks ((config->>'token')) WHERE type = 'email' AND deleted_at IS NULL;
```

(Postgres only — SQLite path falls back to a sequential scan, which is fine since email checks are rare.)

## 6. Wiring

`server/internal/app/server.go`:

```go
emailCheckHandler := emailcheck.NewHandler(s.dbService, s.jobSvc)
s.services.JMAPInbox.RegisterHandler(emailCheckHandler)
```

No new routes — ingestion happens entirely through the JMAP inbox manager.

## 7. Read-only API surface

`GET /api/v1/orgs/$org/checks/$check` already returns `config` for the check. The frontend will compute the address from `check.config.token` + the `addressDomain` exposed by `GET /api/v1/system/parameters/email_inbox` (non-secret view) — handled in spec 03.

For convenience, also extend the existing `/api/v1/check-types/samples` to include a representative email sample so the create-check wizard can pre-populate a name/slug.

## 8. Errors

| Error | Action |
|-------|--------|
| Unknown token in recipient | log warn, return `OutcomeRejected` (move to Processed) — keeps inbox clean. |
| Token belongs to non-email check | log error, return `OutcomeRejected`. |
| Email has no recipient matching token regex | return `OutcomeIgnored` (let other handlers run, eventually trash via cleanup). |
| DB error while saving result | return error → manager logs, leaves email in inbox for retry. |

## 9. Edge cases

| Scenario | Behaviour |
|----------|-----------|
| Same email forwarded twice with same `Message-ID` | New result saved each time. We do NOT dedupe — receiving the same status twice is harmless and the second arrival is a real signal. |
| Email arrives during a maintenance window | Worker / incident layer already filters maintenance windows; we just save the result. |
| Check disabled | Result is still saved (mirrors heartbeat: incoming pings on a disabled check are still recorded). Worker does not run, but ingestion does. |
| Check deleted | `GetCheckByEmailToken` returns nil → `OutcomeRejected`, email goes to Processed. |
| Multiple checks with the same token (shouldn't happen) | DB lookup returns one row arbitrarily; treat as data corruption — log, reject. The migration's index is non-unique by design (history preservation), but `Validate` should reject duplicates by checking the partial index before insert. |

## 10. Tests

| Test | Approach |
|------|----------|
| Token + status extraction | Table-driven: plus-address, header, subject, none, ambiguous (plus and header disagree → plus wins). |
| `HandleEmail` happy path | In-memory DB + check fixture; assert result row + outcome `Processed`. |
| Unknown token | Assert `OutcomeRejected`, no result written. |
| Wrong check type | Assert `OutcomeRejected` + warn log. |
| Worker passive flow | Existing `checkheartbeat` worker test extended to cover `email` (parameterized). |
| End-to-end | Integration test using the fake JMAP server from spec 01: enqueue an email, run one sync cycle, assert result row + email moved to `Processed`. |

## 11. Verification

```bash
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Create an email check
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"type":"email","name":"Backup Job","period":"01:00:00"}' \
  'http://localhost:4000/api/v1/orgs/default/checks' | jq '{slug, config}'

# Note the token, then send an email to <token>@ingest.solidping.example
# from any client. Within seconds, the JMAP listener should pick it up.

curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/checks/backup-job?with=last_result' | jq '.lastResult'

# Send a failure: From: alerts@example.com → To: <token>+down@ingest.solidping.example
# Or: subject containing "[DOWN]". Last result should flip to status=4 (down).
```

## 12. Files to create / modify

| File | Status |
|------|--------|
| `server/internal/checkers/checkerdef/types.go` | add `CheckTypeEmail` |
| `server/internal/checkers/checkemail/checker.go` | new |
| `server/internal/checkers/checkemail/config.go` | new |
| `server/internal/checkers/checkemail/checker_test.go` | new |
| `server/internal/checkers/registry/registry.go` | register checker + config |
| `server/internal/checkworker/worker.go` | generalize heartbeat short-circuit to `isPassive`, rename helper |
| `server/internal/checkworker/worker_test.go` | add `email` parameterization |
| `server/internal/handlers/emailcheck/handler.go` | new |
| `server/internal/handlers/emailcheck/handler_test.go` | new |
| `server/internal/db/service.go` | add `GetCheckByEmailToken` |
| `server/internal/migrations/NNNN_email_token_index.sql` | new partial index |
| `server/internal/app/server.go` | construct + register `emailcheck.Handler` |

---

## Implementation Plan

Sequential commits, each independently buildable + tested.

### Step 1 — `checkerdef.CheckTypeEmail` registration

Add the new check type and its registry entry. Trivial change but unlocks the rest of the work.

### Step 2 — `checkemail` package

`config.go` (`EmailConfig{Token string}` with FromMap/GetConfig), `checker.go` (`EmailChecker` returning `ErrNotExecutable` from Execute, generating a 24-byte hex token in Validate when missing). Mirror `checkheartbeat` exactly. Tests for token generation and validate idempotency.

### Step 3 — Registry + sample

Add `checkemail` to `registry.go` (GetChecker + ParseConfig switches). Optionally extend `/api/v1/check-types/samples` for the wizard.

### Step 4 — Worker passive generalization

Rename `executeHeartbeatJob` → `executePassiveJob` in `internal/checkworker/worker.go`. Add `isPassive(checkType)` returning true for both `Heartbeat` and `Email`. Update existing tests to parameterize over both types. Output messages parameterized on type (`"Email received"` vs `"Heartbeat received"`).

### Step 5 — `db.Service.GetCheckByEmailToken`

Interface method on `db.Service`. Implementations:
- Postgres: `WHERE type = 'email' AND config->>'token' = ? AND deleted_at IS NULL`
- SQLite: `WHERE type = 'email' AND json_extract(config, '$.token') = ? AND deleted_at IS NULL`

Migration `NNNN_email_token_index.sql` adds a partial index for postgres only (SQLite path is a sequential scan; rare).

### Step 6 — `emailcheck.Handler`

`internal/handlers/emailcheck/handler.go` implements the `jmap.Handler` interface:
- `extractTokenAndStatus(email)` — recipient regex `^([0-9a-f]{48})(\+(up|down|error|running))?@`. Header `X-SolidPing-Status` and subject tag `[DOWN]`/etc. as fallbacks.
- `HandleEmail`: extract → lookup → save result → run incident processing.
- `recordResult` mirrors `heartbeat.Service.ReceiveHeartbeat`. Skip incidents for `running`.

Tests: token+status extraction (table-driven), happy path with in-memory DB, unknown token → Rejected, wrong check type → Rejected.

### Step 7 — App wiring

`internal/app/server.go`: build `emailcheck.NewHandler(s.dbService, ...)`, call `s.jmapManager.RegisterHandler(...)`. Done before `Start` so the handler is in place when the manager begins running.

### Step 8 — QA + archive + merge

`make build-backend lint-back test` clean. Move spec to `specs/done/2026/04/`. Open PR with `automerge` label.
