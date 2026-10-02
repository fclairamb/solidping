package msteams

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

func TestBuildIncidentLinesCover(t *testing.T) {
	t.Parallel()

	ctx, svc, _ := setupService(t)
	conn := newConnection(ctx, t, svc, "teams-cover-lines", testTenantID)

	check := models.NewCheck(conn.OrganizationUID, "api", "http")
	require.NoError(t, svc.db.CreateCheck(ctx, check))

	resolvedAt := time.Now().Add(-time.Minute)
	resolved := models.NewIncident(conn.OrganizationUID, check.UID, time.Now().Add(-time.Hour), "r")
	resolved.State = models.IncidentStateResolved
	resolved.ResolvedAt = &resolvedAt

	noEnd := models.NewIncident(conn.OrganizationUID, check.UID, time.Now().Add(-time.Hour), "n")
	noEnd.State = models.IncidentStateResolved

	active := models.NewIncident(conn.OrganizationUID, check.UID, time.Now().Add(-time.Hour), "a")
	orphan := models.NewIncident(conn.OrganizationUID, "missing-check", time.Now(), "o")

	h := &Handler{svc: svc}
	lines := h.buildIncidentLines(ctx, conn.OrganizationUID,
		[]*models.Incident{resolved, noEnd, active, orphan})

	require.Len(t, lines, 4)
	require.Contains(t, lines[0], "🟢")
	require.Contains(t, lines[0], "duration")
	require.NotContains(t, lines[1], "duration")
	require.Contains(t, lines[2], "🔴")
	require.Contains(t, lines[2], "`api`")
	require.Contains(t, lines[3], "``")
}

func TestClientConstructorsCover(t *testing.T) {
	t.Parallel()

	multi := NewClient("app", "secret", "https://smba.example.com/emea/")
	require.Equal(t, DefaultTokenURL, multi.tokenURL)
	require.NotEmpty(t, multi.ServiceURL())
	require.False(t, strings.HasSuffix(multi.ServiceURL(), "//"))

	single := NewClientForTenant("app", "secret", "https://smba.example.com", "tenant-1")
	require.Contains(t, single.tokenURL, "tenant-1")

	noTenant := NewClientForTenant("app", "secret", "https://smba.example.com", "")
	require.Equal(t, DefaultTokenURL, noTenant.tokenURL)
}

func TestConversationNameCover(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		act  Activity
		want string
	}{
		{"channel name", Activity{ChannelData: &ChannelData{Channel: &TeamsChannelInfo{Name: "alerts"}}}, "alerts"},
		{"team fallback", Activity{ChannelData: &ChannelData{Team: &TeamsTeamInfo{Name: "Ops"}}}, "Ops"},
		{"conversation", Activity{Conversation: &ConversationAccount{Name: "chat"}}, "chat"},
		{"empty", Activity{}, ""},
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, tt.act.ConversationName(), tt.name)
	}
}

func TestDownloadManifestAndStartLinkCover(t *testing.T) {
	t.Parallel()

	ctx, svc, _ := setupService(t)
	conn := newConnection(ctx, t, svc, "teams-cover-link", testTenantID)
	org, err := svc.db.GetOrganization(ctx, conn.OrganizationUID)
	require.NoError(t, err)

	h := NewHandler(svc, svc.cfg)

	// Manifest: served with an app id, refused without.
	rec := httptest.NewRecorder()
	require.NoError(t, h.DownloadManifest(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/m.zip", nil)))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	require.NotEmpty(t, rec.Body.Bytes())

	svc.cfg.MSTeams.AppID = ""
	rec = httptest.NewRecorder()
	require.NoError(t, h.DownloadManifest(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/m.zip", nil)))
	require.Equal(t, http.StatusConflict, rec.Code)
	svc.cfg.MSTeams.AppID = testAppID

	link := func(ctx context.Context, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/link", strings.NewReader(body))
		rec := httptest.NewRecorder()
		require.NoError(t, h.StartLink(rec, req))

		return rec
	}

	withOrg := context.WithValue(ctx, base.ContextKeyOrganization, org)

	require.Equal(t, http.StatusNotFound, link(ctx, "").Code, "no org in context")
	require.Equal(t, http.StatusOK, link(withOrg, "").Code, "new unlinked connection")
	require.Equal(t, http.StatusUnprocessableEntity, link(withOrg, "{nope").Code)
	require.Equal(t, http.StatusNotFound, link(withOrg, `{"connectionUid":"does-not-exist"}`).Code)
	require.Equal(t, http.StatusConflict, link(withOrg, `{"connectionUid":"`+conn.UID+`"}`).Code, "already linked")
}
