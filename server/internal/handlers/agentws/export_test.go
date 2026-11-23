package agentws

import (
	"log/slog"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
)

// SetLogger swaps the handler's logger so external tests can assert on what
// the connection path logs (e.g. the keepalive `stale` WARN).
func (h *Handler) SetLogger(logger *slog.Logger) {
	h.logger = logger
}

// SetRejectWarn swaps the "agent result rejected" throttle, so a test can
// drive its clock.
func (h *Handler) SetRejectWarn(throttle *agentcrypto.DeprecationThrottle) {
	h.rejectWarn = throttle
}

// RejectWarnLen returns how many keys the "agent result rejected" throttle
// tracks.
func (h *Handler) RejectWarnLen() int {
	return h.rejectWarn.Len()
}
