package checkerdef_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

var (
	errALPNRefused    = errors.New("remote error: tls: no application protocol")
	errH2CReadHTTP1   = errors.New("http2: frame header looked like an HTTP/1.1 header")
	errH2CConnClosed  = errors.New("http2: client conn could not be established")
	errConnectRefused = errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
)

func TestIsProtocolNegotiationError(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		want bool
	}{
		"nil":                {err: nil, want: false},
		"alpn refused":       {err: errALPNRefused, want: true},
		"h2c read http/1":    {err: errH2CReadHTTP1, want: true},
		"h2c conn closed":    {err: errH2CConnClosed, want: true},
		"connection refused": {err: errConnectRefused, want: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, checkerdef.IsProtocolNegotiationError(tt.err))
		})
	}
}
