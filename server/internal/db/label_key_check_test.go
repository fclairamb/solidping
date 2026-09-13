package db_test

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portLabelKeyCheckPG is distinct from every other embedded-Postgres port
// claimed in the repo.
const portLabelKeyCheckPG = 15508

// errNotALabelViolation is an ordinary failure, for the negative control on
// the classifier.
var errNotALabelViolation = errors.New("connection reset by peer")

// TestLabelKeyCheckParity_SQLite pins the label key/value CHECK against the
// SQLite engine, after migration 021 brought it onto the Postgres rule.
func TestLabelKeyCheckParity_SQLite(t *testing.T) {
	t.Parallel()

	tempDir, err := os.MkdirTemp("", "sqlite-label-key-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: tempDir})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	testLabelKeyCheckParity(t, svc)
}

// TestLabelKeyCheckParity_Postgres is the real-engine twin, and the one that
// matters most: Postgres is where a non-conformant key has always failed, with
// a raw SQLSTATE for a message. The two drivers word the violation completely
// differently — Postgres names the CONSTRAINT and a SQLSTATE, SQLite names the
// constraint with no SQLSTATE — so neither wording proves the other.
func TestLabelKeyCheckParity_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	svc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portLabelKeyCheckPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	testLabelKeyCheckParity(t, svc)
}

// testLabelKeyCheckParity is the engine-agnostic contract: both backends
// refuse exactly the keys models.ValidateLabelKey refuses, both accept the
// ones it accepts, and db.IsLabelConstraintViolation recognizes the refusal on
// either engine — which is what lets the service layer turn a residual
// violation into a validation error instead of leaking the driver string.
func testLabelKeyCheckParity(t *testing.T, svc db.Service) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	org := models.NewOrganization("labelparity", "Acme Label Parity")
	r.NoError(svc.CreateOrganization(ctx, org))

	// Keys the old import regex accepted and no backend may store: too short,
	// leading digit, a dot, and /apply's own legacy managed-label spelling.
	for _, key := range []string{"os", "1abc", "k8s.cluster", "solidping.io/managed", "Env"} {
		_, err := svc.GetOrCreateLabel(ctx, org.UID, key, "value")
		r.Error(err, "key %q must be refused by the database", key)
		r.True(db.IsLabelConstraintViolation(err),
			"the violation for %q must be classifiable, got: %v", key, err)
		r.Error(models.ValidateLabelKey(key),
			"the Go rule and the database rule must agree about %q", key)
	}

	// Positive control: the database accepts exactly what the Go rule accepts,
	// including the new managed-label spelling /apply stamps.
	for _, key := range []string{"env", "environment", "solidping-managed", "a-b-c"} {
		label, err := svc.GetOrCreateLabel(ctx, org.UID, key, "value")
		r.NoError(err, "key %q must be accepted", key)
		r.NotNil(label)
		r.NoError(models.ValidateLabelKey(key))
	}

	// Negative control for the classifier: an unrelated failure is not a label
	// constraint violation, so it can never be reported as one.
	r.False(db.IsLabelConstraintViolation(errNotALabelViolation))
	r.False(db.IsLabelConstraintViolation(nil))
}
