package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbcaptest"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func newCapDB(t *testing.T) *Service {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	return svc
}

func seedCapWorker(t *testing.T, svc *Service, slug string) *models.Worker {
	t.Helper()

	region := "eu-west-1"
	worker, err := svc.RegisterOrUpdateWorker(t.Context(), &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region,
	})
	require.NoError(t, err)

	return worker
}

// reloadCaps reads the column back through the model, which is the only thing
// that proves the round trip preserves the nil/empty distinction.
func reloadCaps(t *testing.T, svc *Service, uid string) *models.Worker {
	t.Helper()

	var row models.Worker
	require.NoError(t, svc.DB().NewSelect().Model(&row).
		Where("uid = ?", uid).Scan(t.Context()))

	return &row
}

// TestWorkerCapabilitiesThreeStatesRoundTrip is THE regression guard for spec
// 2026-08-16-02: NULL, the empty set and a populated set must survive a write
// and a read as three genuinely different values — at the database level, not
// merely in Go. Collapsing NULL into the empty set reintroduces exactly the lie
// spec 2026-08-15-11 was written to remove.
func TestWorkerCapabilitiesThreeStatesRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := newCapDB(t)
	ctx := t.Context()

	unknown := seedCapWorker(t, svc, "wrk-unknown")
	none := seedCapWorker(t, svc, "wrk-none")
	both := seedCapWorker(t, svc, "wrk-both")

	// A worker that never reports: nothing is written at all.
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, unknown.UID, nil))
	// A worker that reported and has none of them.
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, none.UID, []string{}))
	// A worker that reported a set.
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, both.UID, []string{"ipv4", "ipv6"}))

	// The raw column, before the model gets a chance to smooth anything over.
	var rawUnknown, rawNone, rawBoth *string
	for _, spec := range []struct {
		uid  string
		dest **string
	}{{unknown.UID, &rawUnknown}, {none.UID, &rawNone}, {both.UID, &rawBoth}} {
		r.NoError(svc.DB().NewSelect().
			Table("workers").Column("capabilities").
			Where("uid = ?", spec.uid).Scan(ctx, spec.dest))
	}

	r.Nil(rawUnknown, "a worker that never reported must leave the column SQL NULL")
	r.NotNil(rawNone, "a worker that reported none must NOT be stored as NULL")
	r.Equal("[]", *rawNone)
	r.NotNil(rawBoth)
	r.Equal(`["ipv4","ipv6"]`, *rawBoth)

	// And the same three states come back through the model.
	r.Nil(reloadCaps(t, svc, unknown.UID).Capabilities)
	r.Equal([]string{}, reloadCaps(t, svc, none.UID).Capabilities)
	r.Equal([]string{"ipv4", "ipv6"}, reloadCaps(t, svc, both.UID).Capabilities)

	// Which is what the tri-state accessor reports.
	r.Equal(models.CapabilityStateUnknown,
		reloadCaps(t, svc, unknown.UID).Capability(models.CapabilityIPv6))
	r.Equal(models.CapabilityStateAbsent,
		reloadCaps(t, svc, none.UID).Capability(models.CapabilityIPv6),
		"an empty REPORTED set is a real no, not an unknown")
	r.Equal(models.CapabilityStatePresent,
		reloadCaps(t, svc, both.UID).Capability(models.CapabilityIPv6))
}

// TestWorkerCapabilitiesHeartbeatWithoutReportKeepsSet: a heartbeat that says
// nothing must never downgrade a known set back to unknown.
func TestWorkerCapabilitiesHeartbeatWithoutReportKeepsSet(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc := newCapDB(t)
	ctx := t.Context()

	worker := seedCapWorker(t, svc, "wrk-keep")
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, []string{"ipv4", "ipv6"}))

	for range 3 {
		r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, nil))
		r.Equal([]string{"ipv4", "ipv6"}, reloadCaps(t, svc, worker.UID).Capabilities)
	}

	// The registration path carries the same guarantee — an agent hits it on
	// every reconnect, before its first capability-bearing frame.
	again, err := svc.RegisterOrUpdateWorker(ctx, &models.Worker{Slug: "wrk-keep", Name: "wrk-keep"})
	r.NoError(err)
	r.Equal([]string{"ipv4", "ipv6"}, again.Capabilities)

	// But an EXPLICIT empty report is a real statement and does overwrite.
	r.NoError(svc.UpdateWorkerHeartbeat(ctx, worker.UID, []string{}))
	r.Equal([]string{}, reloadCaps(t, svc, worker.UID).Capabilities)
}

// TestWorkerCapabilitiesConstraintParitySQLite runs the shared verdict table
// against the SQLite CHECK + triggers. Its Postgres twin runs the same table.
func TestWorkerCapabilitiesConstraintParitySQLite(t *testing.T) {
	t.Parallel()

	svc := newCapDB(t)
	ctx := t.Context()

	cases := append(dbcaptest.SharedCases(), dbcaptest.SQLiteOnlyCases()...)

	for i, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			slug := fmt.Sprintf("cap-%03d", i)
			worker := seedCapWorker(t, svc, slug)

			// UPDATE exercises the update trigger…
			err := rawSetCaps(ctx, svc, "update workers set capabilities = "+
				tc.SQLiteLiteral+" where uid = ?", worker.UID)
			assertVerdict(t, tc, err, "update")

			// …and a fresh INSERT exercises the insert trigger, which is a
			// separate object and could easily be forgotten.
			err = rawSetCaps(ctx, svc,
				"insert into workers (uid, slug, name, capabilities) values (?, '"+
					slug+"-ins', 'ins', "+tc.SQLiteLiteral+")", uuid.New().String())
			assertVerdict(t, tc, err, "insert")
		})
	}
}

func rawSetCaps(ctx context.Context, svc *Service, query string, arg any) error {
	_, err := svc.DB().ExecContext(ctx, query, arg)

	return err
}

func assertVerdict(t *testing.T, tc dbcaptest.Case, err error, path string) {
	t.Helper()

	if tc.Accepted {
		require.NoError(t, err, "%s: %s must be storable", path, tc.Name)

		return
	}

	require.Error(t, err,
		"%s: %s must be rejected by the database, not merely by the writers", path, tc.Name)
}
