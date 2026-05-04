package incidents

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	errSomethingElse = errors.New("something else")
	errSQLiteDup     = errors.New("UNIQUE constraint failed: incidents.uid")
	errPGDup         = errors.New("duplicate key value violates unique constraint")
	errPGStateCode   = errors.New("pq: SQLSTATE 23505 duplicate key")
)

func TestIsUniqueConstraintError(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.False(isUniqueConstraintError(nil))
	r.False(isUniqueConstraintError(errSomethingElse))
	r.True(isUniqueConstraintError(errSQLiteDup))
	r.True(isUniqueConstraintError(errPGDup))
	r.True(isUniqueConstraintError(errPGStateCode))
	r.True(isUniqueConstraintError(fmt.Errorf("wrapped: %w", errSQLiteDup)))
}
