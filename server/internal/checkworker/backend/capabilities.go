package backend

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/egressreport"
)

// Capabilities is what a worker reports about itself: the egress families it
// self-probed, then the plugin check types it loaded (spec 2026-10-17-05
// §17). A plugin type is an `x-` slug, so it is a valid capability name.
func Capabilities(ctx context.Context) []string {
	egressCaps := egressreport.Current(ctx)
	pluginTypes := registry.PluginTypes()

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
