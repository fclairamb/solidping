package freebox_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// TestClient_OversizedResponseFails: a base URL that streams an endless body
// fails the call with ErrResponseTooLarge and is not read whole (spec
// 2026-10-20-01).
func TestClient_OversizedResponseFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var written atomic.Int64

	chunk := bytes.Repeat([]byte("x"), 32<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for written.Load() < 64<<20 {
			n, err := w.Write(chunk)
			written.Add(int64(n))

			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, err := freebox.NewClient(srv.URL, "").Authorize(context.Background(), "SolidPing-Test")
	r.ErrorIs(err, freebox.ErrResponseTooLarge)
	r.Less(written.Load(), int64(9<<20), "the client read far past the cap")
}
