package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// pushoverFake records the form posted to the Pushover messages endpoint.
type pushoverFake struct {
	mu    sync.Mutex
	forms []url.Values
}

func newPushoverFake(t *testing.T) (*pushoverFake, string) {
	t.Helper()

	f := &pushoverFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		f.mu.Unlock()

		_, _ = w.Write([]byte(`{"status":1,"request":"r1"}`))
	}))
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func (f *pushoverFake) only(t *testing.T) url.Values {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.Len(t, f.forms, 1)

	return f.forms[0]
}

func pushoverPayload(settings models.JSONMap) *Payload {
	payload := pagerdutyPayload(eventTypeIncidentCreated)
	payload.Integration = &models.Integration{
		UID: "chan-po", OrganizationUID: "org-1",
		Type:     models.ConnectionTypePushover,
		Settings: settings,
	}

	return payload
}

func TestPushoverSender_DeliversWithEverySettingsKeyPair(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings models.JSONMap
	}{
		{"canonical user_key/api_token", models.JSONMap{"user_key": "u-1", "api_token": "a-1"}},
		{"legacy dashboard user/token", models.JSONMap{"user": "u-1", "token": "a-1"}},
		{"legacy sender userKey/apiToken", models.JSONMap{"userKey": "u-1", "apiToken": "a-1"}},
		{
			"canonical wins over legacy",
			models.JSONMap{"user_key": "u-1", "api_token": "a-1", "user": "old", "token": "old"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			fake, apiURL := newPushoverFake(t)
			sender := &PushoverSender{APIURL: apiURL}

			r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, pushoverPayload(tc.settings)))

			form := fake.only(t)
			r.Equal("a-1", form.Get("token"))
			r.Equal("u-1", form.Get("user"))
			r.Equal("[DOWN] API health", form.Get("title"))
		})
	}
}

func TestPushoverSender_MissingSettingsAreConfigErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings models.JSONMap
		want     error
		setting  string
	}{
		{"no token", models.JSONMap{"user_key": "u-1"}, ErrPushoverAPITokenNotConfigured, "API token"},
		{"no user key", models.JSONMap{"api_token": "a-1"}, ErrPushoverUserKeyNotConfigured, "user key"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			fake, apiURL := newPushoverFake(t)
			sender := &PushoverSender{APIURL: apiURL}

			err := sender.Send(context.Background(), &jobdef.JobContext{}, pushoverPayload(tc.settings))
			r.ErrorIs(err, tc.want)
			r.ErrorIs(err, ErrIntegrationMisconfigured)

			setting, ok := MissingSetting(err)
			r.True(ok)
			r.Equal(tc.setting, setting)

			fake.mu.Lock()
			r.Empty(fake.forms, "a misconfigured integration must not reach the network")
			fake.mu.Unlock()
		})
	}
}

func TestMissingSetting_DeliveryErrorIsNotConfigError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, ok := MissingSetting(errPushoverRequestFailed)
	r.False(ok)
	r.NotErrorIs(errPushoverRequestFailed, ErrIntegrationMisconfigured)
}

func TestNtfySender_ReadsFormKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings func(srvURL string) models.JSONMap
		priority string
		auth     string
	}{
		{
			name: "dashboard server_url, priority and auth_token",
			settings: func(srvURL string) models.JSONMap {
				return models.JSONMap{"server_url": srvURL, "topic": "alerts", "priority": 2, "auth_token": "tk"}
			},
			priority: "2",
			auth:     "Bearer tk",
		},
		{
			name: "legacy serverUrl and accessToken",
			settings: func(srvURL string) models.JSONMap {
				return models.JSONMap{"serverUrl": srvURL, "topic": "alerts", "accessToken": "old"}
			},
			priority: "urgent",
			auth:     "Bearer old",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			var (
				mu      sync.Mutex
				gotPath string
				gotPrio string
				gotAuth string
			)

			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()

				gotPath = req.URL.Path
				gotPrio = req.Header.Get("Priority")
				gotAuth = req.Header.Get("Authorization")
			}))
			t.Cleanup(srv.Close)

			payload := pagerdutyPayload(eventTypeIncidentCreated)
			payload.Integration = &models.Integration{
				UID: "chan-ntfy", Type: models.ConnectionTypeNtfy, Settings: tc.settings(srv.URL),
			}

			r.NoError((&NtfySender{}).Send(context.Background(), &jobdef.JobContext{}, payload))

			mu.Lock()
			defer mu.Unlock()

			r.Equal("/alerts", gotPath)
			r.Equal(tc.priority, gotPrio)
			r.Equal(tc.auth, gotAuth)
		})
	}
}
