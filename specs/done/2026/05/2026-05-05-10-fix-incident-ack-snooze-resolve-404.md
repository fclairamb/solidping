# Fix incident Ack / Snooze / Resolve buttons returning 404

**Status:** todo
**Owner:** backend
**Severity:** high — three of the most prominent incident actions are dead in the dashboard

## Problem

On an incident detail page, clicking **Acknowledge**, **Snooze**, or **Resolve** always
results in a 404:

```
POST /api/v1/orgs/default/incidents/1e317e15-4f51-44da-82bb-182ebd933389/ack
→ 404 {"title":"Incident not found","code":"NOT_FOUND"}
```

All five mutation endpoints are affected: `/ack`, `/unack`, `/snooze`, `/unsnooze`, `/resolve`.

## Root cause

Parameter-type confusion in the incidents service. The methods declare an `orgUID`
parameter but the HTTP handler passes the org **slug** (e.g. `"default"`), not the
organization UUID.

`server/internal/handlers/incidents/handler.go:249-271` (AcknowledgeIncident):

```go
orgSlug := req.Param("org")               // "default"
incidentUID := req.Param("uid")
incident, err := h.svc.AcknowledgeIncident(req.Context(), orgSlug, &AcknowledgeIncidentRequest{...})
```

`server/internal/handlers/incidents/service.go:1519`:

```go
func (s *Service) AcknowledgeIncident(
    ctx context.Context, orgUID string, req *AcknowledgeIncidentRequest,
) (*models.Incident, error) {
    incident, err := s.db.GetIncident(ctx, orgUID, req.IncidentUID)
    // db.GetIncident runs: WHERE organization_uid = $1 — expects a UUID, gets "default"
    // → sql.ErrNoRows → ErrIncidentNotFound → 404
```

Compare with `GetIncident` (the GET handler, which works) at service.go:1422 — it
correctly resolves the slug first via `s.db.GetOrganizationBySlug(ctx, orgSlug)`
before calling `s.db.GetIncident(ctx, org.UID, ...)`.

The mutation methods skipped that step.

### Caller inconsistency

Five call sites currently feed these methods, with mixed semantics:

| Caller | What it passes | Status |
|---|---|---|
| HTTP handler (web buttons) — handler.go:261, 279, 321, 334, 355 | slug | ❌ broken |
| `tryEmailAck` magic-link path — service.go:1477 | slug | ❌ broken (silent — returns `bool`) |
| `AcknowledgeIncidentFromSlack` — service.go:1627 | `conn.OrganizationUID` (UUID) | ✅ works |
| Auto-unsnooze loop — service.go:1926 | `inc.OrganizationUID` (UUID) | ✅ works |

The Slack path was tested in dev, the HTTP path never was — see the test gap below.

## Fix

Mirror `GetIncident`'s pattern: the **public** service methods accept an `orgSlug`
and resolve it to a UID internally. The internal callers that already have a UID
use a private helper.

### Service changes — `server/internal/handlers/incidents/service.go`

For each of `AcknowledgeIncident`, `UnacknowledgeIncident`, `SnoozeIncident`,
`UnsnoozeIncident`, `ResolveIncident`:

1. Rename the parameter `orgUID` → `orgSlug` in the public method signature.
2. At the top of the method, resolve the org:
   ```go
   org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
   if err != nil {
       return nil, ErrOrganizationNotFound
   }
   ```
3. Use `org.UID` for the rest of the method body (`s.db.GetIncident`, event creation, etc.).
4. Extract a private `*ByOrgUID` helper containing the original logic (post-org-resolution),
   so internal callers that already have a UID don't pay for an extra slug lookup:
   ```go
   func (s *Service) acknowledgeIncidentByOrgUID(ctx context.Context, orgUID string, req *AcknowledgeIncidentRequest) (*models.Incident, error) { /* original body, minus org lookup */ }
   ```
5. Update the public method to call the helper after resolving slug → UID.

### Internal caller updates

- `AcknowledgeIncidentFromSlack` (service.go:1622) — currently calls
  `s.AcknowledgeIncident(ctx, orgUID, ...)`. Change to call
  `s.acknowledgeIncidentByOrgUID(ctx, orgUID, ...)` instead. (No external behaviour change;
  the Slack handler still passes `conn.OrganizationUID`.)
- Auto-unsnooze loop (service.go:1926) — currently calls
  `s.UnsnoozeIncident(ctx, inc.OrganizationUID, ...)`. Change to
  `s.unsnoozeIncidentByOrgUID(ctx, inc.OrganizationUID, ...)`.
- `tryEmailAck` (service.go:1474) — currently passes `orgSlug`, which means after
  the fix it will work via the public method automatically. **Verify once more
  that the magic-link handler at handler.go:207 (`AcknowledgeIncidentByLink`) is
  passing a slug** — it pulls `orgSlug := req.Param("org")` so this is fine.

## Tests

This bug shipped because no integration test exercised the HTTP path. We need
to close that hole.

### `server/internal/handlers/incidents/handler_test.go` (new file)

Table-driven test covering each of the five endpoints with at minimum:

- **Happy path** — existing incident, action succeeds, returns 200 and the
  updated incident; subsequent state on the row is correct (e.g. `acknowledged_at`
  set, `snoozed_until` set, `state = resolved`).
- **404 for non-existent incident** — valid org slug, unknown incident UID.
- **404 for non-existent org** — unknown slug.
- **Snooze-specific:** invalid duration string returns 400 with
  `VALIDATION_ERROR`; `until` in the past returns 400.

Use the standard handler-test setup with testcontainers PostgreSQL (matching
the convention in other handler tests). All test functions must call
`t.Parallel()` and use `testify/require`, per CLAUDE.md.

### `server/test/integration/incidents_test.go`

The existing file is empty (only the package declaration). Add at least one
end-to-end test that:

1. Logs in as `admin@solidping.com`.
2. Triggers an incident (or seeds one in the DB).
3. Calls `POST /api/v1/orgs/default/incidents/{uid}/ack` and asserts 200.
4. Calls `/snooze` with `{"duration":"1h"}` and asserts 200 + state.
5. Calls `/resolve` and asserts 200 + state transition to resolved.

This is the test that, had it existed, would have caught this bug at PR time.

## Out of scope

- **Typed slug/UID aliases.** The bug class — passing one string where another
  was expected — would be prevented by `type OrgSlug string` / `type OrgUID string`.
  Worth considering as a follow-up across the codebase, but introducing it now
  is broad surface change. Note it in the spec backlog instead.
- Frontend changes. The dashboard URLs are correct; this is purely a backend bug.

## Acceptance criteria

- [ ] All five POST endpoints (`/ack`, `/unack`, `/snooze`, `/unsnooze`, `/resolve`)
      return 200 for a real incident under `org=default` (verified via curl + dashboard click-test).
- [ ] Magic-link ack (the email "Acknowledge" button) still works after the refactor.
- [ ] Slack ack still works (no regression in the `AcknowledgeIncidentFromSlack` path).
- [ ] Auto-unsnooze loop still fires for expired snoozes (no regression).
- [ ] New `handler_test.go` covers the five endpoints — happy path + 404 cases.
- [ ] New integration test in `server/test/integration/incidents_test.go` exercises the dashboard click flow.
- [ ] `make lint` and `make test` are green.
