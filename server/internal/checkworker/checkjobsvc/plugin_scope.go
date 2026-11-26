package checkjobsvc

import (
	"context"
	"slices"
	"strings"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
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

// legacyCheckTypes are the built-in types an agent that predates the
// `check-type-*` capability report is assumed to run: the types of the last
// release without it (v0.55.0). FROZEN: a new built-in type is never added
// here, so a legacy agent is never handed a job it has no parser for.
//
//nolint:gochecknoglobals // frozen read-only list
var legacyCheckTypes = []string{
	string(checkerdef.CheckTypeHTTP), string(checkerdef.CheckTypeTCP),
	string(checkerdef.CheckTypeICMP), string(checkerdef.CheckTypeDNS),
	string(checkerdef.CheckTypeHeartbeat), string(checkerdef.CheckTypeEmail),
	string(checkerdef.CheckTypeDomain), string(checkerdef.CheckTypeSSL),
	string(checkerdef.CheckTypeSMTP), string(checkerdef.CheckTypeUDP),
	string(checkerdef.CheckTypeSSH), string(checkerdef.CheckTypePOP3),
	string(checkerdef.CheckTypeIMAP), string(checkerdef.CheckTypeWebSocket),
	string(checkerdef.CheckTypePostgreSQL), string(checkerdef.CheckTypeFTP),
	string(checkerdef.CheckTypeSFTP), string(checkerdef.CheckTypeJS),
	string(checkerdef.CheckTypeMySQL), string(checkerdef.CheckTypeRedis),
	string(checkerdef.CheckTypeMongoDB), string(checkerdef.CheckTypeMSSQL),
	string(checkerdef.CheckTypeOracle), string(checkerdef.CheckTypeClickHouse),
	string(checkerdef.CheckTypeGRPC), string(checkerdef.CheckTypeKafka),
	string(checkerdef.CheckTypeMQTT), string(checkerdef.CheckTypeA2S),
	string(checkerdef.CheckTypeMinecraft), string(checkerdef.CheckTypeRabbitMQ),
	string(checkerdef.CheckTypeSNMP), string(checkerdef.CheckTypeDocker),
	string(checkerdef.CheckTypeSystemd), string(checkerdef.CheckTypeBrowser),
	string(checkerdef.CheckTypeFreeboxLine), string(checkerdef.CheckTypeDNSBL),
	string(checkerdef.CheckTypeSIP), string(checkerdef.CheckTypeKubernetes),
	string(checkerdef.CheckTypeNTP), string(checkerdef.CheckTypeRDP), string(checkerdef.CheckTypeVNC),
	string(checkerdef.CheckTypePrometheus), string(checkerdef.CheckTypePrivateLocation),
	string(checkerdef.CheckTypeCrawl), string(checkerdef.CheckTypeHealth),
	string(checkerdef.CheckTypeVendorStatus), string(checkerdef.CheckTypeSleep),
}

// LegacyCheckTypes returns a copy of the frozen legacy list.
func LegacyCheckTypes() []string {
	return slices.Clone(legacyCheckTypes)
}

// CheckTypesFromCapabilities picks the built-in check types out of an agent's
// capability report (`check-type-<type>`). Empty means the agent did not report
// any: a legacy agent, or one that sent no capabilities at all.
func CheckTypesFromCapabilities(capabilities []string) []string {
	var out []string

	for _, name := range capabilities {
		if slug, ok := strings.CutPrefix(name, models.CheckTypeCapabilityPrefix); ok && slug != "" {
			out = append(out, decodeCheckTypeSlug(slug))
		}
	}

	return out
}

// checkTypeScope keeps a built-in job away from an agent that cannot run its
// type. Plugin types are left to pluginTypeScope; a NULL type stays claimable.
func checkTypeScope(query *bun.SelectQuery, types []string) *bun.SelectQuery {
	if len(types) == 0 {
		types = legacyCheckTypes
	}

	return query.WhereGroup(" AND ", func(group *bun.SelectQuery) *bun.SelectQuery {
		return group.WhereOr("type IS NULL").
			WhereOr("type LIKE ?", checkerdef.PluginTypePrefix+"%").
			WhereOr("type IN (?)", bun.List(types))
	})
}

// decodeCheckTypeSlug maps a capability slug back to the check type: a type
// such as `freebox_line` is reported as `freebox-line`, a capability name being
// `[a-z0-9-]+`. A slug that matches no known type is kept as is.
func decodeCheckTypeSlug(slug string) string {
	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		if CheckTypeSlug(string(checkType)) == slug {
			return string(checkType)
		}
	}

	return slug
}

// CheckTypeSlug is the part of a check type's capability name after the
// prefix: the type with `_` turned into `-`.
func CheckTypeSlug(checkType string) string {
	return strings.ReplaceAll(checkType, "_", "-")
}

// builtinTypesKey carries the built-in check types an agent can run.
type builtinTypesKey struct{}

// WithBuiltinTypes returns a context advertising the built-in check types the
// worker can run, reported as `check-type-*` capabilities.
func WithBuiltinTypes(ctx context.Context, types []string) context.Context {
	return context.WithValue(ctx, builtinTypesKey{}, types)
}

// BuiltinTypesFromContext returns the built-in types advertised on ctx.
func BuiltinTypesFromContext(ctx context.Context) []string {
	types, _ := ctx.Value(builtinTypesKey{}).([]string)

	return types
}
