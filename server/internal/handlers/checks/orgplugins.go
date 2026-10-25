package checks

import (
	"context"
	"log/slog"
	"time"

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

// normalizeConfigFor is normalizeCheckConfig plus the org's agent types.
func (s *Service) normalizeConfigFor(
	ctx context.Context, orgUID, checkType string, effective map[string]any,
) (map[string]any, error) {
	cfg, ok := s.parseConfigFor(ctx, orgUID, checkType)
	if !ok {
		return effective, nil
	}

	return checkerdef.NormalizeConfigFor(cfg, effective) //nolint:wrapcheck // same contract as normalizeCheckConfig
}

// validatePeriodFor is validatePeriodForType plus the org's agent types,
// held to the min/max period their agent reported.
func (s *Service) validatePeriodFor(
	ctx context.Context, orgUID, checkType string, period time.Duration, internal bool, config checkerdef.Config,
) error {
	if remote := s.orgPluginType(ctx, orgUID, checkType); remote != nil {
		meta := remote.Meta()

		return validatePeriodBounds(checkType, period, internal, config, &meta)
	}

	return validatePeriodForType(checkType, period, internal, config)
}

// documentTypes is the org's agent-type lookup for the config-as-code
// validator and importer.
func (s *Service) documentTypes(ctx context.Context, orgUID string) docTypes {
	return docTypes{remote: func(checkType string) *plugins.RemoteType {
		return s.orgPluginType(ctx, orgUID, checkType)
	}}
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
