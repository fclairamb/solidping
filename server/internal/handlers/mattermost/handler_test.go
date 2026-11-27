package mattermost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

type fakeStore struct {
	org        *models.Organization
	connection *models.Integration
	orgErr     error
	connErr    error
}

func (f *fakeStore) GetOrganization(_ context.Context, _ string) (*models.Organization, error) {
	return f.org, f.orgErr
}

func (f *fakeStore) GetChannel(_ context.Context, _ string) (*models.Integration, error) {
	return f.connection, f.connErr
}

type fakeIncidents struct {
	incident  *models.Incident
	check     *models.Check
	ackCalled bool
	ackUser   string
	ackConn   string
}

func (f *fakeIncidents) GetIncidentByUID(_ context.Context, _, _ string) (*models.Incident, error) {
	return f.incident, nil
}

func (f *fakeIncidents) GetCheckByUID(_ context.Context, _, _ string) (*models.Check, error) {
	return f.check, nil
}

func (f *fakeIncidents) AcknowledgeIncidentFromMattermost(
	_ context.Context, _, _, userID, username, integrationUID string,
) (*models.Incident, error) {
	f.ackCalled = true
	f.ackUser = username + "/" + userID
	f.ackConn = integrationUID

	now := time.Now()
	updated := *f.incident
	updated.AcknowledgedAt = &now

	return &updated, nil
}

func newActionTest(t *testing.T, incident *models.Incident) (*Handler, *fakeStore, *fakeIncidents, *config.Config) {
	t.Helper()

	name := "API health"
	cfg := &config.Config{
		Auth:   config.AuthConfig{JWTSecret: "test-secret"},
		Server: config.ServerConfig{BaseURL: "https://sp.acme.com"},
	}
	store := &fakeStore{
		org: &models.Organization{UID: "org-1", Slug: "acme", Name: "Acme"},
		connection: &models.Integration{
			UID: "conn-1", OrganizationUID: "org-1",
			Type: models.ConnectionTypeMattermost, Enabled: true,
		},
	}
	svc := &fakeIncidents{
		incident: incident,
		check:    &models.Check{UID: "chk-1", Name: &name, Type: "http"},
	}

	return NewHandler(store, svc, cfg), store, svc, cfg
}

func mattermostActionToken(t *testing.T, cfg *config.Config) string {
	t.Helper()

	return incidentlinks.SignMattermostAction(
		[]byte(cfg.Auth.JWTSecret), "org-1", "inc-1", "conn-1",
		notifications.MattermostActionAcknowledge, time.Now().Add(time.Hour),
	)
}

func actionRequest(t *testing.T, token, action string) *http.Request {
	t.Helper()

	body, err := json.Marshal(postActionRequest{
		UserID: "mm-user-1", UserName: "alice", Type: "button",
		Context: map[string]any{"token": token, "action": action},
	})
	require.NoError(t, err)

	return httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/integrations/mattermost/actions", strings.NewReader(string(body)),
	)
}

func decodeActionResponse(t *testing.T, rec *httptest.ResponseRecorder) postActionResponse {
	t.Helper()

	require.Equal(t, http.StatusOK, rec.Code)

	var resp postActionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	return resp
}

func TestHandleActionAcknowledge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	incident := &models.Incident{
		UID: "inc-1", OrganizationUID: "org-1", CheckUID: "chk-1",
		State: models.IncidentStateActive, StartedAt: time.Now().Add(-time.Minute),
	}
	handler, _, svc, cfg := newActionTest(t, incident)

	req := actionRequest(t, mattermostActionToken(t, cfg),
		notifications.MattermostActionAcknowledge)
	rec := httptest.NewRecorder()
	r.NoError(handler.HandleAction(rec, req))

	resp := decodeActionResponse(t, rec)
	r.Contains(resp.EphemeralText, "@alice")
	r.Contains(rec.Body.String(), `"mm_blocks"`)
	r.Contains(rec.Body.String(), `"mm_blocks_actions"`)
	r.Contains(rec.Body.String(), `"ephemeral_text"`)
	r.NotNil(resp.Update)
	r.NotEmpty(resp.Update.Props.Blocks)
	r.True(svc.ackCalled)
	r.Equal("alice/mm-user-1", svc.ackUser)
	r.Equal("conn-1", svc.ackConn)
}

func TestHandleActionRejectsCrossActionToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	incident := &models.Incident{
		UID: "inc-1", OrganizationUID: "org-1", CheckUID: "chk-1",
		State: models.IncidentStateActive, StartedAt: time.Now().Add(-time.Minute),
	}
	handler, _, svc, cfg := newActionTest(t, incident)

	req := actionRequest(t, mattermostActionToken(t, cfg),
		"other")
	rec := httptest.NewRecorder()
	r.NoError(handler.HandleAction(rec, req))

	resp := decodeActionResponse(t, rec)
	r.Contains(resp.EphemeralText, "not valid")
	r.False(svc.ackCalled)
}

func TestHandleActionRejectsInvalidToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	incident := &models.Incident{UID: "inc-1", State: models.IncidentStateActive}
	handler, _, svc, _ := newActionTest(t, incident)

	req := actionRequest(t, "forged", notifications.MattermostActionAcknowledge)
	rec := httptest.NewRecorder()
	r.NoError(handler.HandleAction(rec, req))

	resp := decodeActionResponse(t, rec)
	r.Contains(resp.EphemeralText, "invalid or has expired")
	r.False(svc.ackCalled)
}

func TestHandleActionRefusesAlreadyAcknowledged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	now := time.Now()
	incident := &models.Incident{
		UID: "inc-1", State: models.IncidentStateActive, AcknowledgedAt: &now,
	}
	handler, _, svc, cfg := newActionTest(t, incident)

	req := actionRequest(t, mattermostActionToken(t, cfg),
		notifications.MattermostActionAcknowledge)
	rec := httptest.NewRecorder()
	r.NoError(handler.HandleAction(rec, req))

	resp := decodeActionResponse(t, rec)
	r.Contains(resp.EphemeralText, "already acknowledged")
	r.False(svc.ackCalled)
}

func TestHandleActionRefusesResolvedIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	incident := &models.Incident{UID: "inc-1", State: models.IncidentStateResolved}
	handler, _, svc, cfg := newActionTest(t, incident)

	req := actionRequest(t, mattermostActionToken(t, cfg),
		notifications.MattermostActionAcknowledge)
	rec := httptest.NewRecorder()
	r.NoError(handler.HandleAction(rec, req))

	resp := decodeActionResponse(t, rec)
	r.Contains(resp.EphemeralText, "already resolved")
	r.False(svc.ackCalled)
}

func TestHandleActionRefusesDisabledIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	incident := &models.Incident{UID: "inc-1", State: models.IncidentStateActive}
	handler, store, svc, cfg := newActionTest(t, incident)
	store.connection.Enabled = false

	req := actionRequest(t, mattermostActionToken(t, cfg),
		notifications.MattermostActionAcknowledge)
	rec := httptest.NewRecorder()
	r.NoError(handler.HandleAction(rec, req))

	resp := decodeActionResponse(t, rec)
	r.Contains(resp.EphemeralText, "disabled")
	r.False(svc.ackCalled)
}

func TestHandleActionRefusesUnknownIntegration(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	incident := &models.Incident{UID: "inc-1", State: models.IncidentStateActive}
	handler, store, svc, cfg := newActionTest(t, incident)
	store.connection = nil

	req := actionRequest(t, mattermostActionToken(t, cfg),
		notifications.MattermostActionAcknowledge)
	rec := httptest.NewRecorder()
	r.NoError(handler.HandleAction(rec, req))

	resp := decodeActionResponse(t, rec)
	r.Contains(resp.EphemeralText, "no longer exists")
	r.False(svc.ackCalled)
}
