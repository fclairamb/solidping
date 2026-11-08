package jobdef

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

var errBadInput = errors.New("bad input")

func TestPermanentError(t *testing.T) {
	t.Parallel()

	base := errBadInput
	err := fmt.Errorf("wrapped: %w", NewPermanentError(base))

	require.True(t, IsPermanent(err))
	require.False(t, IsRetryable(err))
	require.ErrorIs(t, err, base)
	require.False(t, IsPermanent(base))
}
