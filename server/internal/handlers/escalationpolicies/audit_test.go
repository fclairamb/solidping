package escalationpolicies_test

// End-to-end proof that a real service, backed by a real database, writes the
// audit trail the spec asks for (2026-08-21-09) — actor attribution included.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/escalationpolicies"
)

func newAuditTestService(t *testing.T) (*escalationpolicies.Service, db.Service, *models.Organization) {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)

	t.Cleanup(func() { _ = dbSvc.Close() })

	require.NoError(t, dbSvc.Initialize(t.Context()))

	org := models.NewOrganization("acmeaudit", "Acme")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))

	return escalationpolicies.NewService(dbSvc), dbSvc, org
}

// actorCtx builds the context a request would carry once the auth middleware
// and the request-meta middleware have both run.
func actorCtx(t *testing.T, userUID string) context.Context {
	t.Helper()

	return audit.WithUser(
		audit.WithRequest(t.Context(), "203.0.113.7", "Mozilla/5.0"),
		userUID, models.ActorTypeUser,
	)
}

func listAudit(t *testing.T, dbSvc db.Service, orgUID string) []*models.Event {
	t.Helper()

	rows, err := dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID:   orgUID,
		EventTypePrefixes: []string{"escalation_policy"},
		Limit:             100,
	})
	require.NoError(t, err)

	return rows
}

// TestPolicyLifecycleIsAudited walks create → update → delete and asserts each
// step left exactly one attributed row.
func TestPolicyLifecycleIsAudited(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newAuditTestService(t)

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(t.Context(), user))

	ctx := actorCtx(t, user.UID)

	policy, err := svc.CreatePolicy(ctx, &escalationpolicies.CreatePolicyInput{
		OrganizationUID: org.UID,
		Name:            "primary",
		Steps: []escalationpolicies.StepInput{
			{DelaySeconds: 0, Targets: []escalationpolicies.TargetInput{}},
		},
	})
	r.NoError(err)

	created := listAudit(t, dbSvc, org.UID)
	r.Len(created, 1)
	r.Equal(models.EventTypeEscalationPolicyCreated, created[0].EventType)
	r.Equal(models.ActorTypeUser, created[0].ActorType)
	r.NotNil(created[0].ActorUID)
	r.Equal(user.UID, *created[0].ActorUID)
	r.NotNil(created[0].SourceIP)
	r.Equal("203.0.113.7", *created[0].SourceIP)
	r.NotNil(created[0].UserAgent)
	r.Equal("escalation_policy", created[0].Payload[audit.PayloadKeyTargetType])
	r.Equal(policy.UID, created[0].Payload[audit.PayloadKeyTargetUID])
	r.Equal("primary", created[0].Payload[audit.PayloadKeyTargetName])

	renamed := "secondary"
	_, err = svc.UpdatePolicy(ctx, org.UID, policy.UID, &escalationpolicies.UpdatePolicyInput{
		Name: &renamed,
	})
	r.NoError(err)

	rows := listAudit(t, dbSvc, org.UID)
	r.Len(rows, 2)

	updated := rows[0] // newest first
	r.Equal(models.EventTypeEscalationPolicyUpdated, updated.EventType)
	r.Contains(updated.Payload[audit.PayloadKeyChangedFields], "name")

	changes, ok := updated.Payload[audit.PayloadKeyChanges].(map[string]any)
	r.True(ok, "a scalar rename must carry its from/to pair")

	nameChange, ok := changes["name"].(map[string]any)
	r.True(ok)
	r.Equal("primary", nameChange["from"])
	r.Equal("secondary", nameChange["to"])

	r.NoError(svc.DeletePolicy(ctx, org.UID, policy.UID))

	rows = listAudit(t, dbSvc, org.UID)
	r.Len(rows, 3)
	r.Equal(models.EventTypeEscalationPolicyDeleted, rows[0].EventType)
	r.Equal(policy.UID, rows[0].Payload[audit.PayloadKeyTargetUID])
}

// TestNoOpUpdateWritesNoAuditEvent. A trail that records a PATCH which changed
// nothing is a trail nobody reads — and worse, it lets a real change hide in
// the noise. The positive control is the second half: a genuine change to the
// SAME policy does produce a row, so this is not passing because emission is
// broken.
func TestNoOpUpdateWritesNoAuditEvent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newAuditTestService(t)

	ctx := actorCtx(t, "")

	policy, err := svc.CreatePolicy(ctx, &escalationpolicies.CreatePolicyInput{
		OrganizationUID: org.UID,
		Name:            "primary",
	})
	r.NoError(err)

	same := "primary"
	_, err = svc.UpdatePolicy(ctx, org.UID, policy.UID, &escalationpolicies.UpdatePolicyInput{Name: &same})
	r.NoError(err)

	rows := listAudit(t, dbSvc, org.UID)
	r.Len(rows, 1, "re-sending the current name is not a change")
	r.Equal(models.EventTypeEscalationPolicyCreated, rows[0].EventType)

	// Positive control.
	changed := "renamed"
	_, err = svc.UpdatePolicy(ctx, org.UID, policy.UID, &escalationpolicies.UpdatePolicyInput{Name: &changed})
	r.NoError(err)

	rows = listAudit(t, dbSvc, org.UID)
	r.Len(rows, 2)
	r.Equal(models.EventTypeEscalationPolicyUpdated, rows[0].EventType)
}

// TestAuditEventFallsBackToSystemActor — an internal caller with no request
// context must still produce a schema-valid row rather than a failed insert
// (the actor_type CHECK constraint enumerates a closed set).
func TestAuditEventFallsBackToSystemActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newAuditTestService(t)

	_, err := svc.CreatePolicy(t.Context(), &escalationpolicies.CreatePolicyInput{
		OrganizationUID: org.UID,
		Name:            "internal",
	})
	r.NoError(err)

	rows := listAudit(t, dbSvc, org.UID)
	r.Len(rows, 1)
	r.Equal(models.ActorTypeSystem, rows[0].ActorType)
	r.Nil(rows[0].ActorUID)
	r.Nil(rows[0].SourceIP)
}
