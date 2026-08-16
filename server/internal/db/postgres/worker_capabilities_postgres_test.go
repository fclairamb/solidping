package postgres

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbcaptest"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portWorkerCapabilities is distinct from every other _postgres_test.go
// embedded port in the repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portWorkerCapabilities = 15472

// newCapPG boots the embedded Postgres shared by the capability subtests.
// Self-skips under `-short` (the default `make test` / CI mode), mirroring the
// other embedded-Postgres tests.
//
// ONE instance for all of them, deliberately: concurrent embedded-postgres
// starts race on the shared extraction directory's pwfile, and the loser
// SKIPS. A skipped constraint-parity test proves nothing, so the whole file
// shares a single boot and runs its cases as subtests.
func newCapPG(t *testing.T, port uint32) *Service {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := New(ctx, &Config{Embedded: true, Port: port, RunMode: "test"})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return svc
}

func seedCapWorkerPG(t *testing.T, svc *Service, slug string) *models.Worker {
	t.Helper()

	region := "eu-west-1"
	worker, err := svc.RegisterOrUpdateWorker(t.Context(), &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region,
	})
	require.NoError(t, err)

	return worker
}

func reloadCapsPG(t *testing.T, svc *Service, uid string) *models.Worker {
	t.Helper()

	var row models.Worker
	require.NoError(t, svc.DB().NewSelect().Model(&row).
		Where("uid = ?", uid).Scan(t.Context()))

	return &row
}

// TestWorkerCapabilities_Postgres is the Postgres half of the
// central regression guard: `text[]` NULL, `'{}'` and a populated array must
// come back as three distinguishable values. Postgres and SQLite encode the set
// completely differently (a native array versus a JSON string), so neither
// dialect's proof carries over to the other.
func TestWorkerCapabilities_Postgres(t *testing.T) {
	t.Parallel()

	svc := newCapPG(t, portWorkerCapabilities)

	t.Run("three-states-round-trip", func(t *testing.T) { capThreeStatesPG(t, svc) })
	t.Run("heartbeat-keeps-set", func(t *testing.T) { capHeartbeatKeepsSetPG(t, svc) })
	t.Run("constraint-parity", func(t *testing.T) { capConstraintParityPG(t, svc) })
}

func capThreeStatesPG(t *testing.T, svc *Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	unknown := seedCapWorkerPG(t, svc, "cap-unknown")
	none := seedCapWorkerPG(t, svc, "cap-none")
	both := seedCapWorkerPG(t, svc, "cap-both")

	r.NoError(svc.UpdateWorkerHeartbeat(ctx, unknown.UID, nil))
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, none.UID, []string{}))
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, both.UID, []string{"ipv4", "ipv6"}))

	// The raw column: NULL is NULL, and the empty set is emphatically not.
	for _, spec := range []struct {
		uid    string
		isNull bool
		text   string
	}{
		{unknown.UID, true, ""},
		{none.UID, false, "{}"},
		{both.UID, false, "{ipv4,ipv6}"},
	} {
		var raw *string
		r.NoError(svc.DB().NewSelect().
			Table("workers").ColumnExpr("capabilities::text").
			Where("uid = ?", spec.uid).Scan(ctx, &raw))

		if spec.isNull {
			r.Nil(raw, "a worker that never reported must leave the column SQL NULL")

			continue
		}

		r.NotNil(raw)
		r.Equal(spec.text, *raw)
	}

	r.Nil(reloadCapsPG(t, svc, unknown.UID).Capabilities)
	r.Equal([]string{}, reloadCapsPG(t, svc, none.UID).Capabilities,
		"'{}' must decode to a NON-NIL empty slice, or the empty set silently becomes unknown")
	r.Equal([]string{"ipv4", "ipv6"}, reloadCapsPG(t, svc, both.UID).Capabilities)

	r.Equal(models.CapabilityStateUnknown,
		reloadCapsPG(t, svc, unknown.UID).Capability(models.CapabilityIPv6))
	r.Equal(models.CapabilityStateAbsent,
		reloadCapsPG(t, svc, none.UID).Capability(models.CapabilityIPv6))
	r.Equal(models.CapabilityStatePresent,
		reloadCapsPG(t, svc, both.UID).Capability(models.CapabilityIPv6))
}

// capHeartbeatKeepsSetPG: a silent heartbeat never downgrades a known set, an
// explicit empty one does.
func capHeartbeatKeepsSetPG(t *testing.T, svc *Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	worker := seedCapWorkerPG(t, svc, "cap-keep")
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, []string{"ipv4", "ipv6"}))

	for range 3 {
		r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, nil))
		r.Equal([]string{"ipv4", "ipv6"}, reloadCapsPG(t, svc, worker.UID).Capabilities)
	}

	again, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{Slug: "cap-keep", Name: "cap-keep"})
	r.NoError(err)
	r.Equal([]string{"ipv4", "ipv6"}, again.Capabilities)

	r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, []string{}))
	r.Equal([]string{}, reloadCapsPG(t, svc, worker.UID).Capabilities)
}

// capConstraintParityPG runs the SAME shared verdict table as the SQLite twin.
// The two dialects enforce the shape with entirely different machinery — a
// regex CHECK here, json_each triggers there — and this pair is what pins them
// to the same answers.
func capConstraintParityPG(t *testing.T, svc *Service) {
	t.Helper()

	ctx := t.Context()
	cases := append(dbcaptest.SharedCases(), dbcaptest.PostgresOnlyCases()...)

	for i, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			slug := fmt.Sprintf("shape-%03d", i)
			worker := seedCapWorkerPG(t, svc, slug)

			_, err := svc.DB().ExecContext(ctx,
				"update workers set capabilities = "+tc.PostgresLiteral+" where uid = ?",
				worker.UID)

			if tc.Accepted {
				require.NoError(t, err, "%s must be storable", tc.Name)

				return
			}

			require.Error(t, err,
				"%s must be rejected by the database, not merely by the writers", tc.Name)
		})
	}
}
