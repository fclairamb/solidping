package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// sendNtfy sends payload to a fake ntfy server and returns the request headers it received.
func sendNtfy(t *testing.T, payload *Payload) http.Header {
	t.Helper()
	r := require.New(t)

	var got http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	payload.Integration = &models.Integration{
		UID: "chan-1", OrganizationUID: "org-1",
		Type:     models.ConnectionTypeNtfy,
		Settings: models.JSONMap{"server_url": srv.URL, "topic": "solidping-alerts"},
	}

	r.NoError((&NtfySender{}).Send(context.Background(), &jobdef.JobContext{}, payload))
	r.NotNil(got)

	return got
}

// TestNtfySender_ClickIncidentURL asserts the Click header links to the incident.
func TestNtfySender_ClickIncidentURL(t *testing.T) {
	t.Parallel()

	headers := sendNtfy(t, gotifyPayload(eventTypeIncidentCreated, nil))

	require.Equal(t, "https://solidping.acme.com/d/orgs/acme/incidents/018e4a2b-incident", headers.Get("Click"))
}

// TestNtfySender_NoClickWithoutBaseURL asserts no Click header is sent when
// no incident URL can be built. TestNtfySender_ClickIncidentURL is the positive control.
func TestNtfySender_NoClickWithoutBaseURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := gotifyPayload(eventTypeIncidentCreated, nil)
	payload.AppBaseURL = ""

	headers := sendNtfy(t, payload)

	r.Equal("[DOWN] API health", headers.Get("Title"))
	r.Empty(headers.Get("Click"))
}

// TestNtfySender_LegacyAccessTokenAfterMerge pins that a token stored under the
// legacy accessToken key keeps authenticating once that key is a declared
// secret: it now lives in settings_private, and the settings the sender gets
// are the public ones merged with the private envelope.
func TestNtfySender_LegacyAccessTokenAfterMerge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	sealed, err := credentials.SealConnectionSettings(t.Context(), nil, models.ConnectionTypeNtfy, "org-1",
		map[string]any{"server_url": srv.URL, "topic": "solidping-alerts", "accessToken": "tk_legacy"})
	r.NoError(err)
	r.NotContains(sealed.Public, "accessToken", "the legacy token must be split out of the public settings")
	r.NotNil(sealed.Private)

	conn := &models.Integration{
		UID: "chan-1", OrganizationUID: "org-1",
		Type:            models.ConnectionTypeNtfy,
		Settings:        models.JSONMap(sealed.Public),
		SettingsPrivate: sealed.Private,
	}

	merged, err := credentials.OpenConnectionSettings(t.Context(), nil, conn)
	r.NoError(err)
	conn.Settings = models.JSONMap(merged)

	payload := gotifyPayload(eventTypeIncidentCreated, nil)
	payload.Integration = conn

	r.NoError((&NtfySender{}).Send(context.Background(), &jobdef.JobContext{}, payload))
	r.Equal("Bearer tk_legacy", gotAuth)
}
