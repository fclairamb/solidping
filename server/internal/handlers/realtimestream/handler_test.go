package realtimestream_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/realtimestream"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
)

// streamFixture wires a real HTTP server around the stream handler so SSE
// framing, flushing and termination are exercised end-to-end.
type streamFixture struct {
	bus    *notifier.LocalEventNotifier
	hub    *realtime.Hub
	pub    *realtime.Publisher
	server *httptest.Server
}

func newStreamFixture(t *testing.T, orgUID string, claims *auth.Claims) *streamFixture {
	t.Helper()

	bus := notifier.NewLocalEventNotifier()
	hub := realtime.NewHub(bus, 0, nil)
	pub := realtime.NewPublisher(bus, time.Second, nil)
	handler := realtimestream.NewHandler(hub, 50*time.Millisecond, &config.Config{})

	router := bunrouter.New()
	router.GET("/api/v1/orgs/:org/events/stream",
		func(w http.ResponseWriter, req bunrouter.Request) error {
			ctx := req.Context()
			if orgUID != "" {
				ctx = context.WithValue(ctx, base.ContextKeyOrganization,
					&models.Organization{UID: orgUID, Slug: req.Param("org")})
			}
			if claims != nil {
				ctx = context.WithValue(ctx, base.ContextKeyClaims, claims)
			}

			return handler.Stream(w, req.WithContext(ctx))
		})

	server := httptest.NewServer(router)

	fixture := &streamFixture{bus: bus, hub: hub, pub: pub, server: server}
	t.Cleanup(func() {
		server.Close()
		pub.Close()
		hub.Close()
		_ = bus.Close()
	})

	return fixture
}

// streamConn is an open stream connection: a line scanner over the SSE body
// plus the response status/headers. The body is closed via t.Cleanup.
type streamConn struct {
	scanner *bufio.Scanner
	cancel  context.CancelFunc
	status  int
	header  http.Header
}

// openStream connects to the stream and returns the open connection.
func (f *streamFixture) openStream(t *testing.T) *streamConn {
	t.Helper()
	r := require.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		f.server.URL+"/api/v1/orgs/test/events/stream", nil)
	r.NoError(err)

	resp, err := f.server.Client().Do(req)
	r.NoError(err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	return &streamConn{
		scanner: bufio.NewScanner(resp.Body),
		cancel:  cancel,
		status:  resp.StatusCode,
		header:  resp.Header,
	}
}

// nextEvent reads SSE frames until it sees an `event:` line, returning the
// event name and its data line.
func nextEvent(t *testing.T, scanner *bufio.Scanner) (string, string) {
	t.Helper()

	var event string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			return event, strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("stream ended before an event arrived: %v", scanner.Err())

	return "", ""
}

func TestStream_HelloThenHint(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := newStreamFixture(t, "org-uid-1", nil)
	conn := fixture.openStream(t)
	defer conn.cancel()

	r.Equal(http.StatusOK, conn.status)
	r.Equal("text/event-stream", conn.header.Get("Content-Type"))

	event, data := nextEvent(t, conn.scanner)
	r.Equal("hello", event)
	r.JSONEq(`{"protocol":1}`, data)

	// A hint published for this org arrives on the stream.
	fixture.pub.PublishImmediate(context.Background(), "org-uid-1",
		realtime.KindChecks, realtime.KindIncidents)

	event, data = nextEvent(t, conn.scanner)
	r.Equal("hint", event)
	r.JSONEq(`{"kinds":["checks","incidents"]}`, data)
}

func TestStream_OtherOrgHintNotDelivered(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := newStreamFixture(t, "org-uid-1", nil)
	conn := fixture.openStream(t)
	defer conn.cancel()

	event, _ := nextEvent(t, conn.scanner)
	r.Equal("hello", event)

	fixture.pub.PublishImmediate(context.Background(), "other-org", realtime.KindChecks)
	fixture.pub.PublishImmediate(context.Background(), "org-uid-1", realtime.KindJobs)

	// The next event must be the org's own hint — the other org's never shows.
	event, data := nextEvent(t, conn.scanner)
	r.Equal("hint", event)
	r.JSONEq(`{"kinds":["jobs"]}`, data)
}

func TestStream_TokenExpiryClosesStream(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	claims := &auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(200 * time.Millisecond)),
		},
	}
	fixture := newStreamFixture(t, "org-uid-1", claims)
	conn := fixture.openStream(t)
	defer conn.cancel()

	event, _ := nextEvent(t, conn.scanner)
	r.Equal("hello", event)

	// The server closes the stream at token expiry: the scanner drains
	// (possibly through keep-alive pings) and terminates.
	deadline := time.Now().Add(5 * time.Second)
	for conn.scanner.Scan() {
		r.Less(time.Now().UnixNano(), deadline.UnixNano(),
			"stream must close once the access token expires")
	}
	r.NoError(conn.scanner.Err())
}

func TestStream_HubCloseTerminatesStream(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := newStreamFixture(t, "org-uid-1", nil)
	conn := fixture.openStream(t)
	defer conn.cancel()

	event, _ := nextEvent(t, conn.scanner)
	r.Equal("hello", event)

	go fixture.hub.Close()

	deadline := time.Now().Add(5 * time.Second)
	for conn.scanner.Scan() {
		r.Less(time.Now().UnixNano(), deadline.UnixNano(),
			"stream must terminate when the hub shuts down")
	}
	r.NoError(conn.scanner.Err())
}

func TestStream_MissingOrgContextForbidden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := newStreamFixture(t, "", nil) // no org injected into context
	conn := fixture.openStream(t)
	defer conn.cancel()

	r.Equal(http.StatusForbidden, conn.status)
}

func TestStream_MaxConnectionsGuard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	bus := notifier.NewLocalEventNotifier()
	hub := realtime.NewHub(bus, 1, nil)
	handler := realtimestream.NewHandler(hub, time.Second, &config.Config{})

	router := bunrouter.New()
	router.GET("/api/v1/orgs/:org/events/stream",
		func(w http.ResponseWriter, req bunrouter.Request) error {
			ctx := context.WithValue(req.Context(), base.ContextKeyOrganization,
				&models.Organization{UID: "org-uid-1", Slug: "test"})

			return handler.Stream(w, req.WithContext(ctx))
		})
	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
		hub.Close()
		_ = bus.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/v1/orgs/test/events/stream", nil)
	r.NoError(err)
	resp1, err := server.Client().Do(first)
	r.NoError(err)
	t.Cleanup(func() { _ = resp1.Body.Close() })
	r.Equal(http.StatusOK, resp1.StatusCode)

	// Wait until the first stream is registered before probing the guard.
	r.Eventually(func() bool { return hub.ConnectionCount() == 1 }, 2*time.Second, time.Millisecond)

	second, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/v1/orgs/test/events/stream", nil)
	r.NoError(err)
	resp2, err := server.Client().Do(second)
	r.NoError(err)
	defer func() { _ = resp2.Body.Close() }()
	r.Equal(http.StatusServiceUnavailable, resp2.StatusCode)
}
