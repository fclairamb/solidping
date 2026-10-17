package statussubscribers_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/notifications"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// Hostnames the stub resolver answers for, so no test touches real DNS.
const (
	internalHost = "internal.acme.test"
	publicHost   = "hooks.acme.test"
)

// errNoSuchHost is what the stub resolver answers for any other name.
var errNoSuchHost = errors.New("no such host")

// stubLookup resolves the test hostnames: one to a private address, one to a
// public (documentation-range) address, and localhost to loopback.
func stubLookup(_ context.Context, host string) ([]net.IPAddr, error) {
	switch host {
	case internalHost:
		return []net.IPAddr{{IP: net.ParseIP("10.0.0.5")}}, nil
	case publicHost:
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
	case "localhost":
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}

	return nil, errNoSuchHost
}

// TestEndpointSaveFollowsTheEgressPolicy is the save-time regression: every URL
// below passed the old literal-prefix denylist, and an enforcing guard must
// refuse each of them.
func TestEndpointSaveFollowsTheEgressPolicy(t *testing.T) {
	t.Parallel()

	enforcing := egress.New(false, egress.WithLookup(stubLookup))
	permissive := egress.New(true, egress.WithLookup(stubLookup))

	cases := []struct {
		name   string
		guard  *egress.Guard
		url    string
		accept bool
	}{
		{"172.16/12 refused", enforcing, "https://172.16.0.5/h", false},
		{"CGNAT refused", enforcing, "https://100.64.0.1/h", false},
		{"other loopback refused", enforcing, "https://127.0.0.2/h", false},
		{"IPv6 ULA refused", enforcing, "https://[fd00::1]/h", false},
		{"IPv4-mapped IPv6 refused", enforcing, "https://[::ffff:10.0.0.1]/h", false},
		{"numeric host refused", enforcing, "https://2130706433/h", false},
		{"name resolving privately refused", enforcing, "https://" + internalHost + "/h", false},
		{"name resolving publicly accepted", enforcing, "https://" + publicHost + "/h", true},
		{"private target accepted under a permissive policy", permissive, "https://10.1.2.3/h", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			s := newSubSetup(t)

			svc := statussubscribers.NewService(s.dbSvc, nil, statussubscribers.WithEgressGuard(tc.guard))

			created, err := svc.CreateEndpointSubscriber(t.Context(), s.org.Slug, s.page.UID,
				&statussubscribers.CreateEndpointSubscriberRequest{Channel: "webhook", URL: tc.url})

			if tc.accept {
				r.NoError(err)
				r.NotEmpty(created.UID)

				return
			}

			r.ErrorIs(err, statussubscribers.ErrInvalidEndpointURL)
			r.ErrorIs(err, notifications.ErrSenderURLInvalid)
		})
	}
}

// hitCounter is an http.Handler counting the requests it receives.
type hitCounter struct{ hits atomic.Int32 }

func (h *hitCounter) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.hits.Add(1)
	w.WriteHeader(http.StatusOK)
}

// publish runs one fan-out wave through notifier.
func publish(t *testing.T, s *subSetup, notifier *statussubscribers.Notifier) {
	t.Helper()

	notifier.NotifyStatusUpdate(t.Context(), statussubscribers.UpdateEventFor(
		s.page.UID, nil, string(models.StatusUpdateKindInfo),
		"Database degraded", "We are investigating.", nil, "Acme Status", 0))
}

// failureCount reads the stored consecutive-failure counter of a subscriber.
func failureCount(t *testing.T, s *subSetup, uid string) int {
	t.Helper()

	sub, err := s.dbSvc.GetSubscriber(t.Context(), s.page.UID, uid)
	require.NoError(t, err)

	return sub.FailureCount
}

// guardedTLSClient is a client dialing through guard's transport (a clone
// keeps the guarded DialContext) that trusts server's TLS certificate.
func guardedTLSClient(guard *egress.Guard, server *httptest.Server) *http.Client {
	transport := guard.HTTPTransport().Clone()

	serverTransport, ok := server.Client().Transport.(*http.Transport)
	if ok {
		transport.TLSClientConfig = &tls.Config{
			RootCAs:    serverTransport.TLSClientConfig.RootCAs,
			MinVersion: tls.VersionTLS12,
		}
	}

	return &http.Client{Transport: transport}
}

// TestDeliveryRefusedByTheEgressPolicyAtSendTime is the send-time regression:
// a subscriber stored while the policy allowed private targets (or before the
// save-time check existed) must not be delivered to once the policy enforces.
// It uses the production constructor and its own client: the refusal is
// pre-flight, so no TLS trust is needed.
func TestDeliveryRefusedByTheEgressPolicyAtSendTime(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	target := &hitCounter{}
	server := httptest.NewTLSServer(target)
	t.Cleanup(server.Close)

	created := endpointSubscriber(t, s, server.URL+"/hooks/in", "sec")

	notifier := statussubscribers.NewNotifier(
		s.dbSvc, nil, nil, "https://status.example.com", nil, nil, egress.New(false))

	publish(t, s, notifier)
	r.Zero(target.hits.Load(), "a refused endpoint must never be contacted")
	r.Equal(1, failureCount(t, s, created.UID), "a refusal counts against the circuit breaker")

	// Five refusals disable the subscription, and the event says why.
	for range 4 {
		publish(t, s, notifier)
	}

	sub, err := s.dbSvc.GetSubscriber(t.Context(), s.page.UID, created.UID)
	r.NoError(err)
	r.NotNil(sub.DisabledAt)

	events, err := s.dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: s.org.UID,
		EventTypes:      []models.EventType{models.EventTypeStatusSubscriberDisabled},
	})
	r.NoError(err)
	r.Len(events, 1)

	lastError, _ := events[0].Payload["lastError"].(string)
	r.Contains(lastError, notifications.ErrSenderURLInvalid.Error())
	r.Zero(target.hits.Load())
}

// TestDeliveryRedirectIntoAPrivateTargetIsRefused is the redirect regression:
// a public receiver answering 307 to an internal service must not get the
// POST body (and its signature) forwarded there. Hop 1 is on [::1], treated as
// public by the guard; hop 2 is on 127.0.0.1, judged as production would.
func TestDeliveryRedirectIntoAPrivateTargetIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)

	internal := &hitCounter{}
	hop2 := httptest.NewServer(internal)
	t.Cleanup(hop2.Close)

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	r.NoError(err)

	hop1 := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, hop2.URL+"/internal", http.StatusTemporaryRedirect)
	}))
	_ = hop1.Listener.Close()
	hop1.Listener = listener
	hop1.StartTLS()
	t.Cleanup(hop1.Close)

	created := endpointSubscriber(t, s, hop1.URL+"/hooks/redirect", "sec")

	guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")))
	notifier := statussubscribers.NewNotifier(
		s.dbSvc, nil, nil, "https://status.example.com", nil, nil, guard)
	notifier.SetHTTPClient(guardedTLSClient(guard, hop1))

	publish(t, s, notifier)

	r.Zero(internal.hits.Load(), "the redirect hop into a private target must never be dialed")
	r.Equal(1, failureCount(t, s, created.UID), "a refused redirect is a failed delivery")
}

// TestDeliveryReachesAPublicTargetThroughTheGuard is the positive control for
// the two tests above: the same guarded path does deliver, signed, when the
// target is public.
func TestDeliveryReachesAPublicTargetThroughTheGuard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	rec := newReceiver(t)

	created := endpointSubscriber(t, s, rec.server.URL+"/hooks/ok", "sec")

	guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("127.0.0.1")))
	notifier := statussubscribers.NewNotifier(
		s.dbSvc, nil, nil, "https://status.example.com", nil, nil, guard)
	notifier.SetHTTPClient(guardedTLSClient(guard, rec.server))

	publish(t, s, notifier)

	got := rec.captured()
	r.Len(got, 1)
	r.True(strings.HasPrefix(got[0].Signature, servicesig.SignaturePrefix),
		"the delivery must be signed, got %q", got[0].Signature)
	r.Zero(failureCount(t, s, created.UID))
}
