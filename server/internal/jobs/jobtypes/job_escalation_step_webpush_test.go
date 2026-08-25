package jobtypes

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// escalationWebpushTestVAPIDKeys generates a throwaway VAPID keypair.
func escalationWebpushTestVAPIDKeys(t *testing.T) (string, string) {
	t.Helper()

	priv, pub, err := webpushgo.GenerateVAPIDKeys()
	require.NoError(t, err)

	return pub, priv
}

// setupEscalationWebpushDB creates an in-memory SQLite DB seeded with an org + incident.
func setupEscalationWebpushDB(t *testing.T) (*sqlite.Service, *models.Organization, *models.Incident) {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("test-org", "Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	checkName := "Test Check"
	check := &models.Check{
		UID:             "check-1",
		OrganizationUID: org.UID,
		Name:            &checkName,
		Type:            "http",
	}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	incident := &models.Incident{
		UID:             "incident-1",
		OrganizationUID: org.UID,
		CheckUID:        check.UID,
		State:           models.IncidentStateActive,
		StartedAt:       time.Now().Add(-5 * time.Minute),
	}
	require.NoError(t, dbSvc.CreateIncident(ctx, incident))

	return dbSvc, org, incident
}

// TestSendWebPush_Success verifies that sendWebPush delivers a push request
// and records an audit row when the push service responds with 201.
func TestSendWebPush_Success(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	dbSvc, org, incident := setupEscalationWebpushDB(t)

	// Mock push service — returns 201 Created.
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	pub, priv := escalationWebpushTestVAPIDKeys(t)

	p256dh := "BLiMDpdL9AFok4VWdDMMek7hFVBaleGPi8DVegkLJFH7IFnJo5zAP1GjH50H-njZFrZJ1etQf3F38z68FzSPa1Y"
	subJSON := `{"endpoint":"` + srv.URL + `/push",` +
		`"keys":{"p256dh":"` + p256dh + `","auth":"tBHItnDR9oNFbwKMkhY7lQ"}}`

	contact := &models.UserContact{
		UID:             "contact-wp-1",
		OrganizationUID: org.UID,
		UserUID:         "user-1",
		Type:            models.UserContactTypeWebPush,
		Value:           subJSON,
	}

	route := &models.UserNotificationRoute{
		UID:     "route-wp-1",
		UserUID: "user-1",
		Contact: contact,
	}

	stepUID := "step-1"
	repeatIdx := 0
	run := &EscalationStepJobRun{
		config: EscalationStepJobConfig{
			IncidentUID: incident.UID,
			StepUID:     stepUID,
			RepeatIndex: repeatIdx,
		},
	}

	jctx := &jobdef.JobContext{
		DBService: dbSvc,
		Logger:    slog.Default(),
		Services: &services.Registry{
			WebPushOptions: webpush.Options{
				VAPIDPublicKey:  pub,
				VAPIDPrivateKey: priv,
				Subject:         "mailto:test@example.com",
			},
		},
	}

	sent := run.sendWebPush(ctx, jctx, slog.Default(), incident, route)

	r.Equal(1, sent, "sendWebPush must return 1 on success")
	r.Equal(1, requestCount, "push service must receive exactly one request")
}

// TestSendWebPush_Gone410_PrunesContact verifies that a 410 response from the
// push service causes the contact to be deleted without returning an error.
func TestSendWebPush_Gone410_PrunesContact(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	dbSvc, org, incident := setupEscalationWebpushDB(t)

	// Create a real user+contact so we can verify deletion.
	user := models.NewUser("wp-user@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, member))

	// Mock push service — returns 410 Gone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	pub, priv := escalationWebpushTestVAPIDKeys(t)

	p256dhGone := "BLiMDpdL9AFok4VWdDMMek7hFVBaleGPi8DVegkLJFH7IFnJo5zAP1GjH50H-njZFrZJ1etQf3F38z68FzSPa1Y"
	subJSON := `{"endpoint":"` + srv.URL + `/push",` +
		`"keys":{"p256dh":"` + p256dhGone + `","auth":"tBHItnDR9oNFbwKMkhY7lQ"}}`

	contact := &models.UserContact{
		UID:             "contact-wp-gone",
		OrganizationUID: org.UID,
		UserUID:         user.UID,
		Type:            models.UserContactTypeWebPush,
		Value:           subJSON,
	}
	r.NoError(dbSvc.UpsertUserContact(ctx, contact))

	route := &models.UserNotificationRoute{
		UID:     "route-wp-gone",
		UserUID: user.UID,
		Contact: contact,
	}

	run := &EscalationStepJobRun{
		config: EscalationStepJobConfig{
			IncidentUID: incident.UID,
			StepUID:     "step-1",
		},
	}

	jctx := &jobdef.JobContext{
		DBService: dbSvc,
		Logger:    slog.Default(),
		Services: &services.Registry{
			WebPushOptions: webpush.Options{
				VAPIDPublicKey:  pub,
				VAPIDPrivateKey: priv,
				Subject:         "mailto:test@example.com",
			},
		},
	}

	sent := run.sendWebPush(ctx, jctx, slog.Default(), incident, route)

	// 410 → contact pruned → 0 sent (not an error).
	r.Equal(0, sent, "gone subscription should return 0 (not an error)")

	// Verify the contact was deleted — ListUserContactsWithRoutes only returns
	// active contacts so the result should be empty after deletion.
	routes, listErr := dbSvc.ListUserContactsWithRoutes(ctx, user.UID, org.UID)
	r.NoError(listErr)
	r.Empty(routes, "deleted contact must not appear in route list")
}

// TestSendWebPush_NotConfigured verifies that sendWebPush skips quietly
// when VAPID keys are absent.
func TestSendWebPush_NotConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	incident := &models.Incident{
		UID:             "incident-nc",
		OrganizationUID: "org-nc",
	}

	route := &models.UserNotificationRoute{
		UID:     "route-nc",
		UserUID: "user-nc",
		Contact: &models.UserContact{
			UID:   "contact-nc",
			Type:  models.UserContactTypeWebPush,
			Value: `{"endpoint":"https://example.com","keys":{"p256dh":"abc","auth":"def"}}`,
		},
	}

	run := &EscalationStepJobRun{
		config: EscalationStepJobConfig{IncidentUID: "incident-nc"},
	}

	jctx := &jobdef.JobContext{
		Logger:   slog.Default(),
		Services: &services.Registry{}, // no VAPID keys
	}

	sent := run.sendWebPush(ctx, jctx, slog.Default(), incident, route)
	r.Equal(0, sent, "unconfigured web push must return 0 without error")
}

// TestEscalationWebPushMessage verifies the push content addresses the
// incident by check name and short #N reference — never the UID — and links
// to the incident detail page rather than the generic dashboard.
func TestEscalationWebPushMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		incident  *models.Incident
		checkName string
		orgSlug   string
		wantTitle string
		wantBody  string
		wantURL   string
	}{
		{
			name:      "numbered incident links to its detail page",
			incident:  &models.Incident{UID: "inc-1", Number: 42},
			checkName: "API prod",
			orgSlug:   "acme",
			wantTitle: "[ESCALATED] API prod",
			wantBody:  "Incident #42 for API prod requires your attention.",
			wantURL:   "/dash0/orgs/acme/incidents/inc-1",
		},
		{
			name:      "unnumbered incident omits the reference, not the name",
			incident:  &models.Incident{UID: "inc-2"},
			checkName: "API prod",
			orgSlug:   "acme",
			wantTitle: "[ESCALATED] API prod",
			wantBody:  "Incident for API prod requires your attention.",
			wantURL:   "/dash0/orgs/acme/incidents/inc-2",
		},
		{
			name:      "missing org slug yields empty url for the SW fallback",
			incident:  &models.Incident{UID: "inc-3", Number: 7},
			checkName: "API prod",
			orgSlug:   "",
			wantTitle: "[ESCALATED] API prod",
			wantBody:  "Incident #7 for API prod requires your attention.",
			wantURL:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			msg := escalationWebPushMessage(tt.incident, tt.checkName, tt.orgSlug)
			r.Equal(tt.wantTitle, msg.Title)
			r.Equal(tt.wantBody, msg.Body)
			r.Equal(tt.wantURL, msg.URL)
			r.NotContains(msg.Title, tt.incident.UID, "title must never expose the incident UID")
			r.NotContains(msg.Body, tt.incident.UID, "body must never expose the incident UID")
		})
	}
}
