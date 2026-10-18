package checkjobsvc

import (
	"context"

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
