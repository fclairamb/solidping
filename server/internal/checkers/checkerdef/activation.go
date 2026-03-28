package checkerdef

import "github.com/fclairamb/solidping/server/internal/config"

// ActivationResolver determines which check types are enabled based on server config and org overrides.
type ActivationResolver struct {
	serverEnabled map[CheckType]bool
}

// NewActivationResolver creates a resolver from the server-level checkers configuration.
func NewActivationResolver(cfg config.CheckersConfig) *ActivationResolver {
	allMetas := ListCheckTypeMetas()
	enabled := resolveServerEnabled(cfg, allMetas)

	enabledMap := make(map[CheckType]bool, len(enabled))
	for _, checkType := range enabled {
		enabledMap[checkType] = true
	}

	return &ActivationResolver{serverEnabled: enabledMap}
}

// IsTypeEnabled returns true if the check type is enabled at both server and org level.
func (r *ActivationResolver) IsTypeEnabled(checkType CheckType, orgDisabled []string) bool {
	if !r.serverEnabled[checkType] {
		return false
	}

	for _, disabled := range orgDisabled {
		if CheckType(disabled) == checkType {
			return false
		}
	}

	return true
}

// ListEnabledTypes returns metadata for all types that are enabled (server minus org-disabled).
func (r *ActivationResolver) ListEnabledTypes(orgDisabled []string) []CheckTypeMeta {
	all := ListCheckTypeMetas()
	result := make([]CheckTypeMeta, 0, len(all))

	for idx := range all {
		if r.IsTypeEnabled(all[idx].Type, orgDisabled) {
			result = append(result, all[idx])
		}
	}

	return result
}

// ListAllWithStatus returns all check type metadata annotated with enabled status and reason.
func (r *ActivationResolver) ListAllWithStatus(orgDisabled []string) []CheckTypeStatus {
	all := ListCheckTypeMetas()
	result := make([]CheckTypeStatus, 0, len(all))

	for idx := range all {
		status := CheckTypeStatus{
			CheckTypeMeta: all[idx],
			Enabled:       true,
		}

		if !r.serverEnabled[all[idx].Type] {
			status.Enabled = false
			status.DisabledReason = "server"
		} else {
			for _, disabled := range orgDisabled {
				if CheckType(disabled) == all[idx].Type {
					status.Enabled = false
					status.DisabledReason = "organization"

					break
				}
			}
		}

		result = append(result, status)
	}

	return result
}

// CheckTypeStatus extends CheckTypeMeta with activation status.
type CheckTypeStatus struct {
	CheckTypeMeta
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabledReason,omitempty"`
}

// resolveServerEnabled applies the config precedence rules to determine server-enabled types.
func resolveServerEnabled(cfg config.CheckersConfig, allMetas []CheckTypeMeta) []CheckType {
	// If explicit allowlist is set, use it
	if len(cfg.Enabled) > 0 {
		return intersect(cfg.Enabled, allMetas)
	}

	// Start with all types
	result := make([]CheckType, 0, len(allMetas))

	if len(cfg.EnabledLabels) > 0 {
		// Only include types matching any enabled label
		for idx := range allMetas {
			if allMetas[idx].MatchesLabels(cfg.EnabledLabels) {
				result = append(result, allMetas[idx].Type)
			}
		}
	} else {
		for idx := range allMetas {
			result = append(result, allMetas[idx].Type)
		}
	}

	// Remove disabled types
	if len(cfg.Disabled) > 0 {
		disabledSet := make(map[CheckType]bool, len(cfg.Disabled))
		for _, name := range cfg.Disabled {
			disabledSet[CheckType(name)] = true
		}

		filtered := make([]CheckType, 0, len(result))

		for _, checkType := range result {
			if !disabledSet[checkType] {
				filtered = append(filtered, checkType)
			}
		}

		result = filtered
	}

	return result
}

// intersect returns check types from the allowlist that exist in allMetas.
func intersect(allowlist []string, allMetas []CheckTypeMeta) []CheckType {
	known := make(map[CheckType]bool, len(allMetas))
	for idx := range allMetas {
		known[allMetas[idx].Type] = true
	}

	result := make([]CheckType, 0, len(allowlist))

	for _, name := range allowlist {
		checkType := CheckType(name)
		if known[checkType] {
			result = append(result, checkType)
		}
	}

	return result
}
