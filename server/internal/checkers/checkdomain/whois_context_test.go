package checkdomain

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// stalledWHOISServer accepts port-43 connections and never answers, the way
// a hung WHOIS server does. It returns its "host:port".
func stalledWHOISServer(t *testing.T) string {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			t.Cleanup(func() { _ = conn.Close() }) // held open, silent
		}
	}()

	return listener.Addr().String()
}

// TestExecute_WHOISHonorsContext: the WHOIS path (explicit method and the
// auto fallback after an RDAP failure) used to run on the library's own 30 s
// timeout, ignoring the check context, so the worker abandoned the runner.
// It must now return within the context deadline, as a timeout.
func TestExecute_WHOISHonorsContext(t *testing.T) {
	t.Parallel()

	for _, method := range []string{MethodWHOIS, "auto"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			srv := newFakeRDAPServer(t, []string{"test"}, rdapErrorHandler())
			checker := &DomainChecker{rdapClient: srv.client(), whoisServer: stalledWHOISServer(t)}
			cfg := &DomainConfig{Domain: "acme.test", ThresholdDays: 30, Method: method}

			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()

			done := make(chan *checkerdef.Result, 1)

			go func() {
				result, _ := checker.Execute(ctx, cfg)
				done <- result
			}()

			select {
			case result := <-done:
				r.NotNil(result)
				r.Equal(checkerdef.StatusTimeout, result.Status, "output: %#v", result.Output)
			case <-time.After(5 * time.Second):
				r.Fail("Execute ignored its context on a stalled WHOIS server")
			}
		})
	}
}

// TestQueryWHOIS_CanceledContext: a context canceled mid-query cuts the
// connection right away, even without a deadline.
func TestQueryWHOIS_CanceledContext(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := queryWHOIS(ctx, "acme.test", stalledWHOISServer(t))

	r.ErrorIs(err, context.Canceled)
	r.Less(time.Since(start), 5*time.Second)
}
