package notifications

import (
	"net/textproto"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

func TestRetryableSendError(t *testing.T) {
	t.Parallel()

	require.NoError(t, retryableSendError(nil))
	require.True(t, jobdef.IsRetryable(retryableSendError(&textproto.Error{Code: 452, Msg: "4.4.5 Rate limit"})))
	require.False(t, jobdef.IsRetryable(retryableSendError(&textproto.Error{Code: 550, Msg: "5.1.1 Unknown"})))
	require.False(t, jobdef.IsRetryable(retryableSendError(ErrNoValidRecipients)))
}
