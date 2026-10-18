package integrations_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

// newA2ACardServer serves a 1.0 Agent Card and records the Authorization
// header of the last card fetch.
func newA2ACardServer(t *testing.T, lastAuth *atomic.Value) *httptest.Server {
	t.Helper()

	var srv *httptest.Server

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		lastAuth.Store(req.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"name":"acme investigator","description":"Finds root causes",
"supportedInterfaces":[{"url":"`+srv.URL+`/rpc","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
"capabilities":{},"skills":[{"id":"investigate","name":"Investigate","description":"d","tags":[]}]}`)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// TestResolveA2ACard_BeforeAndAfterSaving: the Test button resolves the card
// from unsaved settings, and for a saved integration fills in the stored
// secret the form does not echo back.
func TestResolveA2ACard_BeforeAndAfterSaving(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var lastAuth atomic.Value

	card := newA2ACardServer(t, &lastAuth)
	svc, org, ctx := newGuardedSenderURLSvc(t, nil)

	result, err := svc.ResolveA2ACard(ctx, org.Slug, &integrations.A2ACardRequest{
		Settings: map[string]any{"agent_card_url": card.URL, "auth": "bearer", "token": "s3cret"},
	})
	r.NoError(err)
	r.True(result.Success, result.Error)
	r.Equal("acme investigator", result.Agent.Name)
	r.Equal("1.0", result.Agent.ProtocolVersion)
	r.Equal(card.URL+"/rpc", result.Agent.InterfaceURL)
	r.Len(result.Agent.Skills, 1)
	r.Equal("Investigate", result.Agent.Skills[0].Name)
	r.Equal("Bearer s3cret", lastAuth.Load())

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "a2a", Name: "investigator",
		Settings: map[string]any{"agent_card_url": card.URL, "auth": "bearer", "token": "stored-token"},
	})
	r.NoError(err)
	r.NotContains(created.Settings, "token", "the token is a secret, never echoed back")
	r.Contains(created.SettingsPrivateKeys, "token")

	result, err = svc.ResolveA2ACard(ctx, org.Slug, &integrations.A2ACardRequest{
		IntegrationUID: created.UID,
		Settings:       map[string]any{"agent_card_url": card.URL, "auth": "bearer", "token": ""},
	})
	r.NoError(err)
	r.True(result.Success, result.Error)
	r.Equal("Bearer stored-token", lastAuth.Load())

	test, err := svc.TestIntegration(ctx, org.Slug, created.UID)
	r.NoError(err)
	r.True(test.Success, test.Error)
	r.Contains(test.Detail, "acme investigator")
}

// TestResolveA2ACard_ReportsFailures: a card URL the egress policy refuses is
// a failed result, not an HTTP error.
func TestResolveA2ACard_ReportsFailures(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var lastAuth atomic.Value

	card := newA2ACardServer(t, &lastAuth)
	svc, org, ctx := newGuardedSenderURLSvc(t, egress.New(false))

	result, err := svc.ResolveA2ACard(ctx, org.Slug, &integrations.A2ACardRequest{
		Settings: map[string]any{"agent_card_url": card.URL},
	})
	r.NoError(err)
	r.False(result.Success)
	r.Contains(result.Error, "public")
	r.Nil(lastAuth.Load(), "the loopback card must never be fetched")

	_, err = svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "a2a", Name: "investigator", Settings: map[string]any{"agent_card_url": card.URL},
	})
	r.ErrorIs(err, integrations.ErrInvalidSettings, "a non-public card URL never reaches storage")
}
