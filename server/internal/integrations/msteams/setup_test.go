package msteams

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

const testAppSecret = "test-app-secret"

// setupService builds a Service over an in-memory SQLite database, wired to a
// fake Bot Connector so command replies and proactive sends are observable
// without any Azure credential.
func setupService(t *testing.T) (context.Context, *Service, *fakeConnector) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))

	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
		MSTeams: config.MSTeamsConfig{
			Enabled:   true,
			AppID:     testAppID,
			AppSecret: testAppSecret,
		},
	}

	connector := newFakeConnector(t)

	svc := NewService(dbService, cfg, nil)
	svc.newBotClient = func(_ string) *Client {
		return NewClientWithTokenURL(testAppID, testAppSecret, connector.server.URL, connector.tokenURL())
	}

	return ctx, svc, connector
}

// newConnection creates a msteams-bot connection bound to a tenant.
func newConnection(t *testing.T, ctx context.Context, svc *Service, orgSlug, tenantID string) *models.Integration {
	t.Helper()

	org := models.NewOrganization(orgSlug, "")
	require.NoError(t, svc.db.CreateOrganization(ctx, org))

	conn := models.NewIntegration(org.UID, models.ConnectionTypeMSTeamsBot, "Microsoft Teams")
	conn.Settings = models.JSONMap{"tenant_id": tenantID}
	require.NoError(t, svc.db.CreateChannel(ctx, conn))

	return conn
}

// recordedCall is one request the fake Bot Connector received.
type recordedCall struct {
	Method   string
	Path     string
	Activity Activity
}

// fakeConnector is an in-repo stand-in for the Bot Connector REST API. It
// serves the client-credentials token endpoint and the three activity
// endpoints (send / reply / update), recording everything so tests can assert
// on what the bot actually sent. This is the only way to exercise the
// outbound path — a live Bot Connector round-trip needs an Azure Bot
// registration, which nothing in this repo can supply.
type fakeConnector struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    []recordedCall
	tokenHit int
	// nextStatus, when non-zero, is returned for the next activity call.
	nextStatus int
}

func newFakeConnector(t *testing.T) *fakeConnector {
	t.Helper()

	fake := &fakeConnector{}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		fake.tokenHit++
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-bot-token",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/v3/conversations/", func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		var activity Activity
		_ = json.Unmarshal(body, &activity)

		fake.mu.Lock()
		fake.calls = append(fake.calls, recordedCall{
			Method: req.Method, Path: req.URL.Path, Activity: activity,
		})
		status := fake.nextStatus
		fake.nextStatus = 0
		fake.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "activity-" + activity.Type})
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	// Each fake gets its own token URL, and each test its own app id, so the
	// process-wide token cache is keyed uniquely per test — no global reset
	// (which would race parallel tests) is needed.
	return fake
}

func (f *fakeConnector) tokenURL() string { return f.server.URL + "/oauth/token" }

func (f *fakeConnector) recorded() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]recordedCall, len(f.calls))
	copy(out, f.calls)

	return out
}

// failNext makes the next activity call answer with the given status.
func (f *fakeConnector) failNext(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nextStatus = status
}

// containsText reports whether any recorded activity's text contains needle.
func (f *fakeConnector) containsText(needle string) bool {
	for _, call := range f.recorded() {
		if strings.Contains(call.Activity.Text, needle) {
			return true
		}
	}

	return false
}

// messageActivity builds an inbound message activity for a tenant.
func messageActivity(tenantID, conversationID, text string) *Activity {
	return &Activity{
		Type:         ActivityTypeMessage,
		ID:           "inbound-1",
		ServiceURL:   testServiceURL,
		Text:         text,
		From:         &ChannelAccount{ID: "29:user"},
		Recipient:    &ChannelAccount{ID: "28:" + testAppID},
		Conversation: &ConversationAccount{ID: conversationID, ConversationType: "channel"},
		ChannelData: &ChannelData{
			Tenant:  &TeamsTenantInfo{ID: tenantID},
			Team:    &TeamsTeamInfo{ID: "19:team", Name: "Ops"},
			Channel: &TeamsChannelInfo{ID: conversationID, Name: "alerts"},
		},
	}
}

// installActivity builds an installationUpdate activity.
func installActivity(tenantID, conversationID, action string) *Activity {
	return &Activity{
		Type:         ActivityTypeInstallationUpdate,
		Action:       action,
		ServiceURL:   testServiceURL,
		From:         &ChannelAccount{ID: "29:installer"},
		Recipient:    &ChannelAccount{ID: "28:" + testAppID},
		Conversation: &ConversationAccount{ID: conversationID, ConversationType: "channel"},
		ChannelData: &ChannelData{
			Tenant:  &TeamsTenantInfo{ID: tenantID},
			Team:    &TeamsTeamInfo{ID: "19:team", Name: "Ops"},
			Channel: &TeamsChannelInfo{ID: conversationID, Name: "alerts"},
		},
	}
}
