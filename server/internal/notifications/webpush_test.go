package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// webPushTestVAPIDKeys generates a throwaway VAPID keypair for tests.
func webPushTestVAPIDKeys(t *testing.T) (string, string) {
	t.Helper()

	priv, pub, err := webpushgo.GenerateVAPIDKeys()
	require.NoError(t, err)

	return pub, priv
}

// webPushFakeSub returns a minimal JSON push subscription pointing to the given server URL.
func webPushFakeSub(t *testing.T, serverURL string, label string) map[string]any {
	t.Helper()

	return map[string]any{
		"endpoint": serverURL + "/push",
		"keys": map[string]string{
			"p256dh": "BLiMDpdL9AFok4VWdDMMek7hFVBaleGPi8DVegkLJFH7IFnJo5zAP1GjH50H-njZFrZJ1etQf3F38z68FzSPa1Y",
			"auth":   "tBHItnDR9oNFbwKMkhY7lQ",
		},
		"label": label,
	}
}

// webPushFakeSubJSON returns JSON-encoded subscription for a contact value.
func webPushFakeSubJSON(t *testing.T, serverURL string) string {
	t.Helper()

	sub := webPushFakeSub(t, serverURL, "Chrome on macOS")
	b, err := json.Marshal(sub)
	require.NoError(t, err)

	return string(b)
}

// webPushChannel builds a minimal Channel with the given subscriptions in Settings.
func webPushChannel(t *testing.T, subs ...map[string]any) *models.Integration {
	t.Helper()

	ch := &models.Integration{
		UID:  "ch-test",
		Type: models.ConnectionTypeWebPush,
		Settings: models.JSONMap{
			"subscriptions": subs,
		},
	}

	return ch
}

// webPushJobCtx builds a minimal JobContext with VAPID credentials.
func webPushJobCtx(pub, priv string) *jobdef.JobContext {
	return &jobdef.JobContext{
		Services: &services.Registry{
			WebPushOptions: webpush.Options{
				VAPIDPublicKey:  pub,
				VAPIDPrivateKey: priv,
				Subject:         "mailto:test@example.com",
			},
		},
	}
}

// webPushPayload builds a minimal Payload for WebPushSender.Send.
func webPushPayload(ch *models.Integration) *Payload {
	checkName := "MyCheck"

	return &Payload{
		EventType:   eventTypeIncidentCreated,
		Incident:    &models.Incident{UID: "inc-1"},
		Check:       &models.Check{Name: &checkName},
		Integration: ch,
	}
}

// TestWebPushSender_Send_AllSucceed verifies that a 201 response from the push
// service is treated as a success and UpdateChannel is NOT called.
func TestWebPushSender_Send_AllSucceed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	pub, priv := webPushTestVAPIDKeys(t)

	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusCreated)
	}))

	defer srv.Close()

	ch := webPushChannel(t,
		webPushFakeSub(t, srv.URL, "Chrome on macOS"),
		webPushFakeSub(t, srv.URL, "Firefox on Linux"),
	)

	updateCalled := false
	sender := &WebPushSender{
		UpdateChannel: func(_ context.Context, _ *models.Integration) error {
			updateCalled = true

			return nil
		},
	}

	err := sender.Send(context.Background(), webPushJobCtx(pub, priv), webPushPayload(ch))
	r.NoError(err)
	r.Equal(2, requestCount, "both subscriptions should receive a push")
	r.False(updateCalled, "UpdateChannel must not be called when no subscription is gone")
}

// TestWebPushSender_Send_OneGone410 verifies that a 410 from the push service
// removes that subscription from channel Settings and calls UpdateChannel.
func TestWebPushSender_Send_OneGone410(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	pub, priv := webPushTestVAPIDKeys(t)

	// First server: returns 201 (alive).
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv1.Close()

	// Second server: returns 410 (gone).
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv2.Close()

	ch := webPushChannel(t,
		webPushFakeSub(t, srv1.URL, "Chrome on macOS"),
		webPushFakeSub(t, srv2.URL, "Expired browser"),
	)

	var updatedChannel *models.Integration

	sender := &WebPushSender{
		UpdateChannel: func(_ context.Context, ch *models.Integration) error {
			updatedChannel = ch

			return nil
		},
	}

	err := sender.Send(context.Background(), webPushJobCtx(pub, priv), webPushPayload(ch))
	r.NoError(err)
	r.NotNil(updatedChannel, "UpdateChannel must be called to prune the dead subscription")

	// Verify the gone endpoint was removed from the settings.
	remaining, parseErr := parseSubscriptions(updatedChannel.Settings)
	r.NoError(parseErr)
	r.Len(remaining, 1, "exactly one subscription should remain after pruning")
	r.Contains(remaining[0].Endpoint, srv1.URL, "the surviving endpoint should be srv1")
}

// TestBuildWebPushURL verifies the click-through / tag URL is non-empty and
// points at the incident detail page. A non-empty URL is what gives each
// incident a distinct service-worker tag; an empty one made Chrome reject the
// notification (renotify requires a non-empty tag).
func TestBuildWebPushURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload *Payload
		want    string
	}{
		{
			name:    "incident present",
			payload: &Payload{OrgSlug: "acme", Incident: &models.Incident{UID: "inc-1"}},
			want:    "/dash0/orgs/acme/incidents/inc-1",
		},
		{
			name:    "no incident falls back to org dashboard",
			payload: &Payload{OrgSlug: "acme"},
			want:    "/dash0/orgs/acme",
		},
		{
			name:    "incident without uid falls back to org dashboard",
			payload: &Payload{OrgSlug: "acme", Incident: &models.Incident{}},
			want:    "/dash0/orgs/acme",
		},
		{
			name:    "missing org slug yields empty url",
			payload: &Payload{Incident: &models.Incident{UID: "inc-1"}},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, buildWebPushURL(tt.payload))
		})
	}
}

// TestBuildWebPushContent_IncidentNumber verifies titles carry the short #N
// incident reference (and never the UID), matching Slack/Discord headers.
func TestBuildWebPushContent_IncidentNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType string
		incident  *models.Incident
		wantTitle string
	}{
		{
			name:      "created carries the reference",
			eventType: eventTypeIncidentCreated,
			incident:  &models.Incident{UID: "inc-1", Number: 42},
			wantTitle: "#42 · [DOWN] my-check",
		},
		{
			name:      "resolved carries the reference",
			eventType: eventTypeIncidentResolved,
			incident:  &models.Incident{UID: "inc-1", Number: 42},
			wantTitle: "#42 · [RECOVERED] my-check",
		},
		{
			name:      "unnumbered incident keeps the bare title",
			eventType: eventTypeIncidentCreated,
			incident:  &models.Incident{UID: "inc-1"},
			wantTitle: "[DOWN] my-check",
		},
		{
			name:      "no incident keeps the bare title",
			eventType: eventTypeIncidentResolved,
			wantTitle: "[RECOVERED] my-check",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			payload := &Payload{EventType: tt.eventType, Incident: tt.incident}

			title, body := buildWebPushContent(payload, "my-check")
			r.Equal(tt.wantTitle, title)
			r.NotContains(title, "inc-1", "title must never expose the incident UID")
			r.NotContains(body, "inc-1", "body must never expose the incident UID")
		})
	}
}

// TestWebPushSender_Send_NoVAPIDKeys verifies that the sender returns
// ErrWebPushNotConfigured when VAPID keys are absent.
func TestWebPushSender_Send_NoVAPIDKeys(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	sender := &WebPushSender{}
	ch := webPushChannel(t)

	err := sender.Send(context.Background(), &jobdef.JobContext{
		Services: &services.Registry{},
	}, webPushPayload(ch))

	r.ErrorIs(err, ErrWebPushNotConfigured)
}

// TestWebPushSender_Send_NoSubscriptions verifies that the sender is a no-op
// when the channel has an empty subscriptions array.
func TestWebPushSender_Send_NoSubscriptions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	pub, priv := webPushTestVAPIDKeys(t)

	ch := webPushChannel(t) // no subs

	updateCalled := false
	sender := &WebPushSender{
		UpdateChannel: func(_ context.Context, _ *models.Integration) error {
			updateCalled = true

			return nil
		},
	}

	err := sender.Send(context.Background(), webPushJobCtx(pub, priv), webPushPayload(ch))
	r.NoError(err)
	r.False(updateCalled)
}

// TestWebPushSender_Send_SubJSON verifies that the sender correctly parses
// the subscription JSON for the per-user contact type (Phase A style).
func TestWebPushSender_Send_SubJSON(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	pub, priv := webPushTestVAPIDKeys(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	// Build settings with a JSON-encoded subscription as a string (Phase A stores it differently).
	subJSON := webPushFakeSubJSON(t, srv.URL)

	// Parse back to map for channel settings.
	var subMap map[string]any
	require.NoError(t, json.Unmarshal([]byte(subJSON), &subMap))

	ch := webPushChannel(t, subMap)

	sender := &WebPushSender{}

	err := sender.Send(context.Background(), webPushJobCtx(pub, priv), webPushPayload(ch))
	r.NoError(err)
}
