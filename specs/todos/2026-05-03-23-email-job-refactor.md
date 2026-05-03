# Email — route transactional sends through the email job

## Context

Three transactional emails are sent **synchronously inside HTTP request handlers** today, in `server/internal/handlers/auth/service.go`:

- Registration confirmation — L1491–1511 (called from `Register`)
- Password reset — L1754–1771 (called from `RequestPasswordReset`)
- Invitation — L2358–2389 (`sendInvitationEmail`, called from `InviteMember` at L2030)

Each one calls `s.emailFormatter.Format(...)` then `s.emailSender.Send(ctx, msg)` directly. If the SMTP server is slow or briefly unavailable:

- The API call hangs for the whole TCP+TLS+SMTP roundtrip (often 1–3s, sometimes much more).
- A transient SMTP failure becomes a logged error with **no retry** — the user just doesn't get the email and never knows.
- There's no record of the send in the jobs table, so operators can't audit or replay.

Meanwhile, the project already has a fully-functional **email job** (`server/internal/jobs/jobtypes/job_email.go`):

- `EmailJobConfig` accepts `To/CC/BCC/Subject/Template/TemplateData` — exactly what these call sites need.
- It's wired into `jobdef.JobTypeEmail` and registered in `jobtypes/registry.go:12`.
- It already retries on network errors (`isNetworkError`, `jobdef.NewRetryableError`).
- It uses the same `EmailFormatter` under the hood, so HTML/text output is identical.
- Incident notifications already use the job pattern (`server/internal/handlers/incidents/service.go:931`, `:970`).

This spec converts the three remaining direct-send call sites to enqueue an email job.

## Honest opinion

This is the smallest of the three email specs and the highest-impact. It removes user-visible latency on register/invite/password-reset, gives us free retry on flaky SMTP, and surfaces sends in the existing jobs UI for operator audit — all without writing any new infrastructure.

One real risk: routing password-reset through the worker means a worker outage = no reset emails. I'd ship as-is and only add a sync fallback if it actually bites in practice. Worker outages already break incident notifications (a bigger deal), so adding a second failure mode here doesn't change the operational picture meaningfully.

## Scope

**In:**
- Refactor `Register` (registration confirmation), `RequestPasswordReset`, and `sendInvitationEmail` to enqueue an `email` job instead of calling `emailSender.Send`.
- Inject `jobsvc.Service` into the auth `Service` constructor at `server/internal/handlers/auth/service.go:254`.
- Remove `emailSender` and `emailFormatter` fields from auth `Service` if no other call sites use them after the refactor (grep first — likely yes, they can both go).
- Update `server/internal/app/services/` registry / DI wiring so the auth service receives the jobs service.
- Update tests in `server/internal/handlers/auth/service_test.go` to assert that a job was enqueued (mock `jobsvc.Service.CreateJob`) instead of asserting an email was sent.

**Out:**
- Any change to the templates themselves (covered in `2026-05-03-25-email-templates-polish.md`).
- Any change to the X-Mailer / From headers (covered in `2026-05-03-24-email-mailer-identity.md`).
- Touching `server/internal/notifications/email.go` — that already runs inside a job.
- Touching `server/internal/handlers/system/service.go:198` (admin "send test email" endpoint) — that one should stay synchronous because the operator needs the round-trip result inline.

## Implementation

### 1. Add jobs service to auth `Service`

`server/internal/handlers/auth/service.go:73-74` currently has:

```go
emailSender    email.Sender
emailFormatter email.Formatter
```

Replace with:

```go
jobsSvc jobsvc.Service
```

Update the constructor signature at L254 accordingly. Update `internal/app/server.go` (or the DI wiring point) where the auth service is constructed.

### 2. Replace each direct send with a job enqueue

Helper to keep the call sites tidy (private to the auth service):

```go
func (s *Service) enqueueEmail(
    ctx context.Context, orgUID, to, subject, template string, data any,
) error {
    cfg := jobtypes.EmailJobConfig{
        To:           []string{to},
        Subject:      subject,
        Template:     template,
        TemplateData: data,
    }
    raw, err := json.Marshal(cfg)
    if err != nil {
        return fmt.Errorf("marshalling email job config: %w", err)
    }
    if _, err := s.jobsSvc.CreateJob(ctx, orgUID, string(jobdef.JobTypeEmail), raw, nil); err != nil {
        return fmt.Errorf("enqueueing email job: %w", err)
    }
    return nil
}
```

Call sites:

| Site | orgUID | Subject | Template | Data |
|------|--------|---------|----------|------|
| `Register` (L1491–1511) | `""` (no org yet — `Job.OrganizationUID` is nullable) | `[SolidPing] Confirm your account` | `registration.html` | `{ConfirmURL}` |
| `RequestPasswordReset` (L1754–1771) | `""` (reset is pre-auth, may not know which org) | `[SolidPing] Reset your password` | `password-reset.html` | `{ResetURL}` |
| `sendInvitationEmail` (L2358–2389) | the org UID (already in scope) | `[SolidPing] You're invited to {orgName}` | `invitation.html` | `{OrgName, Role, InviterName, InviteURL}` |

Log and continue on enqueue errors — current behaviour is "log and continue if email fails" and we should preserve that. Don't let a job-creation failure block registration or invitation.

### 3. Tests

Existing tests in `server/internal/handlers/auth/service_test.go` that exercise these flows:

- Should now assert `jobsSvc.CreateJob` was called with the expected `EmailJobConfig` (unmarshal the `config json.RawMessage` and check fields).
- Mock the `jobsvc.Service` interface — see how incidents tests mock it for reference.
- Drop any assertions about `emailSender.Send` being called from these flows.

Add one new test: SMTP failure scenario at the job level — enqueue succeeds, `Register` returns 200, even when (in the test) the email job would fail. This proves we've decoupled the API response from SMTP availability.

## Verification

- `make test` and `make lint` pass.
- Manual end-to-end with a running stack:
  1. `docker-compose up -d` then `make dev`.
  2. Trigger registration: `curl -X POST -H 'Content-Type: application/json' -d '{"email":"newuser@example.com","password":"testpass123","name":"Test"}' http://localhost:4000/api/v1/auth/register`
  3. Confirm response is fast (<200ms), even if SMTP is slow.
  4. `curl -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/jobs?type=email'` and verify the job appears with `status=success`.
- Stop the SMTP container, repeat: API still returns 200; job lands as `retried` then `failed` after retries are exhausted; no 500 to the caller.

## Critical files

- `server/internal/handlers/auth/service.go` — three direct-send call sites.
- `server/internal/jobs/jobtypes/job_email.go` — `EmailJobConfig` struct (do not modify).
- `server/internal/jobs/jobsvc/service.go` — `CreateJob` entry point (signature: `CreateJob(ctx, orgUID, jobType, config, jobOptions)`).
- `server/internal/jobs/jobdef/types.go` — `JobTypeEmail` constant.
- `server/internal/handlers/incidents/service.go:931` — reference example of `CreateJob` from a handler.
- `server/internal/app/server.go` — DI wiring point for auth service construction.
