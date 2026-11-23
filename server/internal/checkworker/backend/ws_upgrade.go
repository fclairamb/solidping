package backend

import (
	"context"

	"github.com/coder/websocket"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/version"
)

// The agent's side of the rolling upgrade (spec 2026-11-22-04). The server
// sends an `upgrade` frame when this agent's version differs from its own; the
// agent stops claiming, finishes its in-flight jobs, closes the socket and
// exits 0, and its supervisor starts it again on the server's image.

// UpgradeRequested is closed when the server asked this agent to upgrade.
func (b *WSBackend) UpgradeRequested() <-chan struct{} {
	return b.upgradeRequested
}

// autoUpgrade is the agent's autoUpgrade announcement: false when pinned.
func (b *WSBackend) autoUpgrade() *bool {
	follow := !b.ignoreVersionDrift

	return &follow
}

// handleUpgradeRequest records the server's request, once. A pinned agent
// (SP_AGENT_IGNORE_VERSION_DRIFT=true) ignores it: an older server may still
// send one.
func (b *WSBackend) handleUpgradeRequest(ctx context.Context, frame *agents.ServerFrame) {
	if b.ignoreVersionDrift {
		b.ignoredUpgradeOnce.Do(func() {
			b.logger.InfoContext(ctx, "server asked this agent to upgrade; ignoring it (SP_AGENT_IGNORE_VERSION_DRIFT=true)",
				"agent_version", version.Get().Version, "server_version", frame.TargetVersion)
		})

		return
	}

	b.upgradeOnce.Do(func() {
		b.logger.InfoContext(ctx, "server asked this agent to upgrade: draining, then exiting",
			"agent_version", version.Get().Version, "server_version", frame.TargetVersion)
		close(b.upgradeRequested)
	})
}

// Close closes the active connection cleanly, without reconnecting. Call it
// once the worker loop stopped.
func (b *WSBackend) Close() {
	b.mu.Lock()
	conn := b.conn
	b.conn = nil
	b.mu.Unlock()

	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "agent exiting")
	}
}
