package slack

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsRecipientError(t *testing.T) {
	t.Parallel()

	for code, want := range map[string]bool{
		"channel_not_found": true, "user_not_found": true, "user_disabled": true,
		"invalid_auth": false, "token_revoked": false, "account_inactive": false,
	} {
		err := fmt.Errorf("wrapped: %w", &APIError{Code: code})
		got, isRecipient := IsRecipientError(err)
		require.Equal(t, code, got)
		require.Equal(t, want, isRecipient, code)
		require.ErrorIs(t, err, ErrSlackAPI)
	}

	_, isRecipient := IsRecipientError(ErrSlackAPI)
	require.False(t, isRecipient)
}
