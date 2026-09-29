package agentws

import "log/slog"

// SetLogger swaps the handler's logger so external tests can assert on what
// the connection path logs (e.g. the keepalive `stale` WARN).
func (h *Handler) SetLogger(logger *slog.Logger) {
	h.logger = logger
}
