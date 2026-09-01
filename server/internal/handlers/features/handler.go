// Package features exposes a minimal feature-flag endpoint so the frontend
// can decide which UI elements to render. Today only the bug-report icon
// looks at it; the package will grow as more conditional features land.
package features

import (
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler returns the active feature flags for the frontend.
type Handler struct {
	base.HandlerBase
	cfg *config.Config
}

// NewHandler constructs a Handler.
func NewHandler(cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		cfg:         cfg,
	}
}

// HeartbeatPush describes the embedded TCP/UDP heartbeat transports to the
// dashboard (spec 2026-09-01-06).
//
// The dashboard renders the netcat / firmware examples only when a listener is
// actually enabled, so it needs to learn both which transports are up and what
// host:port a device should send to. Host and ports are derived from the
// server's own configuration; they are advertising, not authorization —
// nothing here grants access to anything.
type HeartbeatPush struct {
	TCPEnabled bool `json:"tcpEnabled"`
	UDPEnabled bool `json:"udpEnabled"`
	// Host is the hostname a device should send beats to, derived from the
	// configured base URL. Empty when it cannot be derived, in which case the
	// dashboard tells the reader to substitute their own host.
	Host string `json:"host"`
	// TCPPort / UDPPort are 0 when the matching transport is disabled.
	TCPPort int `json:"tcpPort"`
	UDPPort int `json:"udpPort"`
}

// Response is the JSON shape returned by GET /api/v1/features.
type Response struct {
	BugReport     bool          `json:"bugReport"`
	HeartbeatPush HeartbeatPush `json:"heartbeatPush"`
}

// GetFeatures handles GET /api/v1/features (auth required upstream).
func (h *Handler) GetFeatures(writer http.ResponseWriter, _ *http.Request) error {
	return h.WriteJSON(writer, http.StatusOK, Response{
		BugReport:     h.cfg.App.EnableBugReport,
		HeartbeatPush: heartbeatPushFeature(h.cfg),
	})
}

// heartbeatPushFeature summarizes the push-transport configuration.
func heartbeatPushFeature(cfg *config.Config) HeartbeatPush {
	if cfg == nil {
		return HeartbeatPush{}
	}

	return HeartbeatPush{
		TCPEnabled: cfg.Heartbeat.TCPEnabled(),
		UDPEnabled: cfg.Heartbeat.UDPEnabled(),
		Host:       baseURLHost(cfg.Server.BaseURL),
		TCPPort:    listenPort(cfg.Heartbeat.TCPListen),
		UDPPort:    listenPort(cfg.Heartbeat.UDPListen),
	}
}

// listenPort extracts the port from a configured listen address, returning 0
// when the listener is off or the address carries no usable port.
func listenPort(value string) int {
	normalized := config.NormalizeHeartbeatListen(value)
	if normalized == "" {
		return 0
	}

	_, portStr, err := net.SplitHostPort(normalized)
	if err != nil {
		return 0
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}

	return port
}

// baseURLHost returns the hostname of the configured public base URL, without
// its port — the beat listeners live on their own port, so carrying the HTTP
// one over would print a wrong example.
func baseURLHost(baseURL string) string {
	if baseURL == "" {
		return ""
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}

	return parsed.Hostname()
}
