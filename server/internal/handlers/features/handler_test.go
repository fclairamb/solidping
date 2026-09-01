package features_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/features"
)

func getFeatures(t *testing.T, cfg *config.Config) features.Response {
	t.Helper()

	handler := features.NewHandler(cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/features", nil)

	require.NoError(t, handler.GetFeatures(rec, req))
	require.Equal(t, http.StatusOK, rec.Code)

	var out features.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	return out
}

// TestFeaturesReportsHeartbeatPushOff is the default: the dashboard must not
// advertise a transport nobody can reach.
func TestFeaturesReportsHeartbeatPushOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	out := getFeatures(t, &config.Config{})
	r.False(out.HeartbeatPush.TCPEnabled)
	r.False(out.HeartbeatPush.UDPEnabled)
	r.Zero(out.HeartbeatPush.TCPPort)
	r.Zero(out.HeartbeatPush.UDPPort)
}

// TestFeaturesReportsHeartbeatPushOn gives the dashboard everything it needs
// to render a copy-pasteable example, and pins that the HTTP port is NOT
// carried over from the base URL.
func TestFeaturesReportsHeartbeatPushOn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://solidping.example.com:8443"
	cfg.Heartbeat.TCPListen = ":4001"
	cfg.Heartbeat.UDPListen = "4002"

	out := getFeatures(t, cfg)
	r.True(out.HeartbeatPush.TCPEnabled)
	r.True(out.HeartbeatPush.UDPEnabled)
	r.Equal("solidping.example.com", out.HeartbeatPush.Host)
	r.Equal(4001, out.HeartbeatPush.TCPPort)
	r.Equal(4002, out.HeartbeatPush.UDPPort)
}

// TestFeaturesReportsOneTransport covers the asymmetric case an operator who
// only opened UDP would hit.
func TestFeaturesReportsOneTransport(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	cfg.Heartbeat.UDPListen = "true"

	out := getFeatures(t, cfg)
	r.False(out.HeartbeatPush.TCPEnabled)
	r.True(out.HeartbeatPush.UDPEnabled)
	r.Zero(out.HeartbeatPush.TCPPort)
	r.Equal(config.DefaultHeartbeatPushPort, out.HeartbeatPush.UDPPort)
}
