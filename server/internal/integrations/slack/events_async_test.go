package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHandleEventsSurvivesRequestCancel pins that an Events API delivery is
// handled on a context detached from the HTTP request: Slack drops the
// connection after 3 s, which cancels the request context, and the
// chat.unfurl call made with it used to fail with "context canceled".
func TestHandleEventsSurvivesRequestCancel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, svc, _ := solidpingSetup(t)
	calls := coverFakeSlack(t, svc)
	h := &Handler{svc: svc, cfg: svc.cfg}

	body := `{"type":"event_callback","team_id":"` + msgTeamID + `","event":{"type":"link_shared",` +
		`"channel":"` + msgChannel + `","message_ts":"1.1",` +
		`"links":[{"domain":"localhost","url":"http://localhost:4000/d/orgs/acme/checks/api"}]}}`

	// The request context is already canceled: Slack gave up on the delivery.
	reqCtx, cancel := context.WithCancel(t.Context())
	cancel()

	req := httptest.NewRequestWithContext(reqCtx, http.MethodPost, "/events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	r.NoError(h.HandleEvents(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	h.events.Wait()
	r.Equal(1, calls["/chat.unfurl"])
}
