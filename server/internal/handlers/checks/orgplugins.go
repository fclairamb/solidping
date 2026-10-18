package checks

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/plugins"
	"github.com/fclairamb/solidping/server/internal/plugintypes"
)

// orgPluginType returns the plugin type an agent of orgUID reported (spec
// 2026-10-17-05 §18), for a type this server does not know itself. Nil
// otherwise: a built-in type, a type of the operator's plugin directory, or
// a type the org's agents never reported. Always looked up under orgUID, so
// one org's agent types never resolve for another org.
func (s *Service) orgPluginType(ctx context.Context, orgUID, checkType string) *plugins.RemoteType {
	if orgUID == "" || !checkerdef.CheckType(checkType).IsPlugin() ||
		configregistry.IsKnownType(checkerdef.CheckType(checkType)) || s.db == nil || s.db.DB() == nil {
		return nil
	}

	remote, ok, err := plugintypes.New(s.db.DB()).Lookup(ctx, orgUID, checkType)
	if err != nil {
		slog.WarnContext(ctx, "Could not read the org's plugin types", "type", checkType, "error", err)

		return nil
	}

	if !ok {
		return nil
	}

	return remote
}

// isKnownTypeFor is configregistry.IsKnownType plus the org's agent types.
func (s *Service) isKnownTypeFor(ctx context.Context, orgUID, checkType string) bool {
	return configregistry.IsKnownType(checkerdef.CheckType(checkType)) ||
		s.orgPluginType(ctx, orgUID, checkType) != nil
}

// validateSpecFor is configregistry.ValidateSpec plus the org's agent types,
// validated against the schema their agent reported.
func (s *Service) validateSpecFor(
	ctx context.Context, orgUID, checkType string, spec *checkerdef.CheckSpec,
) error {
	if remote := s.orgPluginType(ctx, orgUID, checkType); remote != nil {
		return remote.ValidateSpec(spec)
	}

	return configregistry.ValidateSpec(checkerdef.CheckType(checkType), spec)
}

// parseConfigFor is configregistry.ParseConfig plus the org's agent types, so
// their x-solidping-secret-fields are encrypted at rest like any other.
//
//nolint:ireturn // config factory
func (s *Service) parseConfigFor(ctx context.Context, orgUID, checkType string) (checkerdef.Config, bool) {
	if cfg, ok := configregistry.ParseConfig(checkerdef.CheckType(checkType)); ok {
		return cfg, true
	}

	if remote := s.orgPluginType(ctx, orgUID, checkType); remote != nil {
		return remote.NewConfig(), true
	}

	return nil, false
}
