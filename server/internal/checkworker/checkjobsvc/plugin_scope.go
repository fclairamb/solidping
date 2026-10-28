package checkjobsvc

import (
	"context"
	"slices"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// pluginTypesKey carries the plugin check types a cloud worker loaded.
type pluginTypesKey struct{}

// WithPluginTypes returns a context advertising the plugin check types the
// claiming worker can run (spec 2026-10-17-05 §17). A claim made without it
// advertises none.
func WithPluginTypes(ctx context.Context, types []string) context.Context {
	return context.WithValue(ctx, pluginTypesKey{}, types)
}

// PluginTypesFromContext returns the plugin types advertised on ctx.
func PluginTypesFromContext(ctx context.Context) []string {
	types, _ := ctx.Value(pluginTypesKey{}).([]string)

	return types
}

// PluginTypesFromCapabilities picks the plugin check types out of an agent's
// capability report: a plugin type is a valid capability name (`x-…`).
func PluginTypesFromCapabilities(capabilities []string) []string {
	var out []string

	for _, name := range capabilities {
		if checkerdef.CheckType(name).IsPlugin() {
			out = append(out, name)
		}
	}

	return out
}

// pluginTypeScope keeps a plugin-type job away from a worker that did not load
// that plugin: only the advertised `x-` types are claimable. Built-in types
// are unaffected. A NULL type stays claimable, like excludePassiveTypes.
func pluginTypeScope(query *bun.SelectQuery, types []string) *bun.SelectQuery {
	return query.WhereGroup(" AND ", func(group *bun.SelectQuery) *bun.SelectQuery {
		group = group.WhereOr("type IS NULL").WhereOr("type NOT LIKE ?", checkerdef.PluginTypePrefix+"%")

		if len(types) > 0 {
			group = group.WhereOr("type IN (?)", bun.List(types))
		}

		return group
	})
}

// noICMPKey marks a claim made by a worker that reported it cannot open an
// ICMP socket.
type noICMPKey struct{}

// WithNoICMP returns a context for a claim by a worker whose ICMP probe said
// no: it must not claim icmp jobs (spec 2026-10-28-06).
func WithNoICMP(ctx context.Context, noICMP bool) context.Context {
	return context.WithValue(ctx, noICMPKey{}, noICMP)
}

// noICMPFromContext reads WithNoICMP.
func noICMPFromContext(ctx context.Context) bool {
	noICMP, _ := ctx.Value(noICMPKey{}).(bool)

	return noICMP
}

// NoICMPFromCapabilities reports whether a capability report is a real "no
// ICMP": a non-nil (closed) set without the icmp name. A nil set is "unknown"
// and keeps claiming everything.
func NoICMPFromCapabilities(capabilities []string) bool {
	return capabilities != nil && !slices.Contains(capabilities, "icmp")
}

// icmpScope keeps icmp jobs away from a worker that cannot ping.
func icmpScope(query *bun.SelectQuery, noICMP bool) *bun.SelectQuery {
	if !noICMP {
		return query
	}

	return query.WhereGroup(" AND ", func(group *bun.SelectQuery) *bun.SelectQuery {
		return group.WhereOr("type IS NULL").WhereOr("type <> ?", string(checkerdef.CheckTypeICMP))
	})
}
