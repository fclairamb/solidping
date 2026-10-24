package jobtypes

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// webhookReceiver records what a fake publication-webhook receiver got.
type webhookReceiver struct {
	hits atomic.Int32
	mu   sync.Mutex
	body []byte
}

func (rec *webhookReceiver) lastBody() []byte {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	return rec.body
}

// newWebhookReceiver starts a plain HTTP receiver answering status with body.
func newWebhookReceiver(t *testing.T, status int, body string) (*webhookReceiver, *httptest.Server) {
	t.Helper()

	rec := &webhookReceiver{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits.Add(1)

		got, _ := io.ReadAll(r.Body)

		rec.mu.Lock()
		rec.body = got
		rec.mu.Unlock()

		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	return rec, server
}

// runWebhookJob builds and runs a webhook job against url under guard. A nil
// registry means "services not wired".
func runWebhookJob(t *testing.T, url string, registry *services.Registry) error {
	t.Helper()

	config, err := json.Marshal(WebhookJobConfig{
		URL:  url,
		Body: map[string]any{"event": "publication.updated"},
	})
	require.NoError(t, err)

	runner, err := (&WebhookJobDefinition{}).CreateJobRun(config)
	require.NoError(t, err)

	return runner.Run(t.Context(), &jobdef.JobContext{Logger: slog.Default(), Services: registry})
}

// TestWebhookJobRefusesNonPublicTargets is the send-time regression: an
// enforcing policy refuses a loopback target before any request, and the
// refusal is terminal.
func TestWebhookJobRefusesNonPublicTargets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec, server := newWebhookReceiver(t, http.StatusOK, "ok")

	err := runWebhookJob(t, server.URL+"/hook", &services.Registry{EgressGuard: egress.New(false)})
	r.Error(err)
	r.ErrorIs(err, egress.ErrDenied)
	r.ErrorIs(err, notifications.ErrSenderURLInvalid)
	r.False(jobdef.IsRetryable(err), "a policy refusal must not be retried")
	r.Zero(rec.hits.Load(), "a refused target must never be contacted")
}

// TestWebhookJobRefusesARedirectIntoAPrivateTarget: hop 1 on [::1] is treated
// as public and answers 307 to hop 2 on 127.0.0.1, which the guarded dial
// must refuse.
func TestWebhookJobRefusesARedirectIntoAPrivateTarget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	internal, hop2 := newWebhookReceiver(t, http.StatusOK, "ok")

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "[::1]:0")
	r.NoError(err)

	hop1 := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, hop2.URL+"/internal", http.StatusTemporaryRedirect)
	}))
	_ = hop1.Listener.Close()
	hop1.Listener = listener
	hop1.Start()
	t.Cleanup(hop1.Close)

	guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")))

	err = runWebhookJob(t, hop1.URL+"/hook", &services.Registry{EgressGuard: guard})
	r.Error(err)
	r.ErrorIs(err, egress.ErrDenied)
	r.False(jobdef.IsRetryable(err), "a policy refusal must not be retried")
	r.Zero(internal.hits.Load(), "the redirect hop into a private target must never be dialed")
}

// TestWebhookJobCapsTheStoredResponseBody: the job error is stored as job
// output readable by every member, so only a short snippet of a failing
// receiver's body may end up in it.
func TestWebhookJobCapsTheStoredResponseBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const marker = "END-OF-BODY-MARKER"

	body := strings.Repeat("x", 1<<20) + marker
	_, server := newWebhookReceiver(t, http.StatusInternalServerError, body)

	err := runWebhookJob(t, server.URL+"/hook", &services.Registry{EgressGuard: egress.New(true)})
	r.ErrorIs(err, ErrWebhookHTTPError)
	r.LessOrEqual(len(err.Error()), 600)
	r.NotContains(err.Error(), marker)
	r.Contains(err.Error(), "HTTP 500")
}

// TestWebhookJobDeliversToAllowedTargets is the positive control for the tests
// above: a permissive policy, a public override, and no policy at all each
// still deliver.
func TestWebhookJobDeliversToAllowedTargets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		registry *services.Registry
	}{
		{"permissive policy", &services.Registry{EgressGuard: egress.New(true)}},
		{"loopback treated as public", &services.Registry{
			EgressGuard: egress.New(false, egress.WithPublicOverride(net.ParseIP("127.0.0.1"))),
		}},
		{"no services wired", nil},
		{"no guard installed", &services.Registry{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			rec, server := newWebhookReceiver(t, http.StatusOK, "ok")

			r.NoError(runWebhookJob(t, server.URL+"/hook", tc.registry))
			r.Equal(int32(1), rec.hits.Load())
			r.JSONEq(`{"event":"publication.updated"}`, string(rec.lastBody()))
		})
	}
}
