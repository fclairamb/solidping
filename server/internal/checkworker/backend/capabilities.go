package backend

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkworker/egressreport"
	"github.com/fclairamb/solidping/server/internal/plugins"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

// Capabilities is what a worker reports about itself: the egress families it
// self-probed, then the plugin check types it can run (spec 2026-10-17-05
// §17): the ones it loaded and its activation gate enables (§19). A plugin
// type is an `x-` slug, so it is a valid capability name.
func Capabilities(ctx context.Context, pluginTypes []string) []string {
	egressCaps := egressreport.Current(ctx)

	// Without plugins the set is exactly egressreport's, nil ("unknown")
	// included. With plugins it is always non-nil: the claim needs the types
	// even when the egress probe has not answered yet.
	if len(pluginTypes) == 0 {
		return egressCaps
	}

	out := make([]string, 0, len(egressCaps)+len(pluginTypes))
	out = append(out, egressCaps...)

	return append(out, pluginTypes...)
}

// pluginReport returns the plugin manifests to put on the next claim frame,
// and the connection they are for (spec 2026-10-17-05 §18). The manifests go
// once per connection, not on every claim: a schema is kilobytes and the
// claim frame is the agent's heartbeat. Nil when this connection already got
// them or when no plugin is loaded.
func (b *WSBackend) pluginReport() ([]protocol.TypeManifest, time.Time) {
	manifests := plugins.LoadedTypeManifests()
	if manifests == nil {
		return nil, time.Time{}
	}

	b.mu.Lock()
	connectedAt, reportedFor := b.connectedAt, b.pluginsReportedFor
	b.mu.Unlock()

	if !connectedAt.IsZero() && connectedAt.Equal(reportedFor) {
		return nil, connectedAt
	}

	return fitPluginReport(manifests), connectedAt
}

// maxPluginReportBytes keeps the manifests well under the server's inbound
// frame cap (maxFrameBytes): a frame over it would drop the connection.
const maxPluginReportBytes = maxFrameBytes * 3 / 4

// fitPluginReport keeps the manifests that fit the report budget, in order.
// The types left out still run here; the server just cannot validate them
// while this agent is offline.
func fitPluginReport(manifests []protocol.TypeManifest) []protocol.TypeManifest {
	out := make([]protocol.TypeManifest, 0, len(manifests))
	size := 0

	for i := range manifests {
		raw, err := json.Marshal(&manifests[i])
		if err != nil || size+len(raw) > maxPluginReportBytes || len(out) == plugins.MaxReportedTypes {
			slog.Warn("Plugin type left out of the report to the server", "type", manifests[i].Type)

			continue
		}

		size += len(raw)
		out = append(out, manifests[i])
	}

	return out
}
