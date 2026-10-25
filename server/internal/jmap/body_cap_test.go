package jmap_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jmap"
)

// TestDiscoverSession_OversizedResponseFails: a session URL that streams an
// endless body fails discovery with ErrResponseTooLarge and is not read whole
// (spec 2026-10-20-01).
func TestDiscoverSession_OversizedResponseFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var written atomic.Int64

	chunk := bytes.Repeat([]byte("x"), 32<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for written.Load() < 128<<20 {
			n, err := w.Write(chunk)
			written.Add(int64(n))

			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client := jmap.NewClient(&jmap.Config{SessionURL: srv.URL + "/.well-known/jmap", Username: "alice"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.DiscoverSession(ctx)
	r.ErrorIs(err, jmap.ErrResponseTooLarge)
	r.Less(written.Load(), int64(24<<20), "the client read far past the cap")
}
