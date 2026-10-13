package db

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

var errStub = errors.New("stub")

func TestDescribeHalfMigrated(t *testing.T) {
	t.Parallel()

	for _, msg := range []string{
		"list organizations: SQL logic error: no such column: organization.default_escalation_policy_uid (1)",
		"SQL logic error: no such table: jobs (1)",
		`ERROR: column "region_spread" of relation "checks" does not exist (SQLSTATE 42703)`,
		`ERROR: relation "jobs" does not exist (SQLSTATE 42P01)`,
	} {
		cause := fmt.Errorf("%w: %s", errStub, msg)
		got := DescribeHalfMigrated(cause)

		require.ErrorIs(t, got, ErrHalfMigrated, msg)
		require.ErrorIs(t, got, cause, "the original error stays reachable")
		require.Contains(t, got.Error(), msg)
	}
}

func TestDescribeHalfMigratedLeavesOtherErrorsAlone(t *testing.T) {
	t.Parallel()

	require.NoError(t, DescribeHalfMigrated(nil))

	other := fmt.Errorf("%w: connection refused", errStub)
	require.Equal(t, other, DescribeHalfMigrated(other))
	require.NotErrorIs(t, DescribeHalfMigrated(other), ErrHalfMigrated)

	notSchema := fmt.Errorf("%w: user does not exist", errStub)
	require.Equal(t, notSchema, DescribeHalfMigrated(notSchema))
}
