package backend

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkworker/egressreport"
	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/plugins"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

// Capabilities is what a worker reports about itself: the egress families it
// self-probed, the worker-task executors it runs (spec 2026-10-22-02), then the
// plugin check types it can run (spec 2026-10-17-05 §17): the ones it loaded
// and its activation gate enables (§19). A plugin type is an `x-` slug, so it
// is a valid capability name.
func Capabilities(ctx context.Context, pluginTypes []string) []string {
	egressCaps := WithTaskCapabilities(egressreport.Current(ctx), tasks.Capabilities())

	// Without plugins the set is exactly that, nil ("unknown") included. With
	// plugins it is always non-nil: the claim needs the types even when the
	// egress probe has not answered yet.
	if len(pluginTypes) == 0 {
		return egressCaps
	}

	out := make([]string, 0, len(egressCaps)+len(pluginTypes))
	out = append(out, egressCaps...)

	return append(out, pluginTypes...)
}

// WithTaskCapabilities adds the executors' capability names to a reported
// set. A nil ("unknown") set stays nil: appending to it would turn it into
// "no IPv4/IPv6".
func WithTaskCapabilities(base, taskCaps []string) []string {
	if base == nil || len(taskCaps) == 0 {
		return base
	}

	return append(slices.Clone(base), taskCaps...)
}

// pluginReport returns the plugin manifests to put on the next claim frame,
// and the connection they are for (spec 2026-10-17-05 §18). The manifests go
// once per connection, not on every claim: a schema is kilobytes and the
// claim frame is the agent's heartbeat. Nil when this connection already got
// them or when no plugin is loaded. A change in the loaded types (an http
// plugin's refresh, spec 2026-10-17-06) sends them again on the same
// connection.
func (b *WSBackend) pluginReport() ([]protocol.TypeManifest, pluginReportKey) {
	generation := plugins.LoadedGeneration()

	manifests := plugins.LoadedTypeManifests()
	if manifests == nil {
		return nil, pluginReportKey{}
	}

	b.mu.Lock()
	key := pluginReportKey{connectedAt: b.connectedAt, generation: generation}
	reportedFor := b.pluginsReportedFor
	b.mu.Unlock()

	if !key.connectedAt.IsZero() && key.connectedAt.Equal(reportedFor.connectedAt) &&
		key.generation == reportedFor.generation {
		return nil, key
	}

	return fitPluginReport(manifests), key
}

// pluginReportKey is what one plugin report was for: a connection and a
// generation of the loaded types.
type pluginReportKey struct {
	connectedAt time.Time
	generation  uint64
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
