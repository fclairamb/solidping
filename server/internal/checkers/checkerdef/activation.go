package checkerdef

import "github.com/fclairamb/solidping/server/internal/config"

// ActivationResolver determines which check types are enabled based on server config and org overrides.
type ActivationResolver struct {
	serverEnabled  map[CheckType]bool
	deploymentMode string
}

// NewActivationResolver creates a resolver from the server-level checkers
// configuration. deploymentMode is config.Config.Deployment.Mode
// (config.DeploymentModeSaaS / DeploymentModeSelfHosted); it is process-wide,
// never per-org, which is what lets this resolver stay a single value built
// once at startup and shared by every org (see dockerNoteFor).
func NewActivationResolver(cfg *config.CheckersConfig, deploymentMode string) *ActivationResolver {
	allMetas := ListCheckTypeMetas()
	enabled := resolveServerEnabled(cfg, allMetas)

	enabledMap := make(map[CheckType]bool, len(enabled))
	for _, checkType := range enabled {
		enabledMap[checkType] = true
	}

	return &ActivationResolver{serverEnabled: enabledMap, deploymentMode: deploymentMode}
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

		status.Note = dockerNoteFor(all[idx].Type, r.deploymentMode)

		result = append(result, status)
	}

	return result
}

// dockerNoteFor is the catalog's half of the SaaS docker gate (spec
// 2026-09-25-22). The per-org exception — a docker check is fine in SaaS when
// it is pinned to one of THIS org's private locations — needs the org's own
// regions, which this resolver does not have (it is built once at startup and
// shared by every org, see NewActivationResolver). So the listing does not
// hide `docker` in SaaS mode: doing that would also be wrong for an org that
// does have a private location. Instead it stays listed and enabled, with a
// note the create-time gate (checks.Service.validateDockerDeploymentConfig)
// actually enforces, so the dashboard/MCP caller can explain the constraint
// up front instead of only after a rejected create.
func dockerNoteFor(checkType CheckType, deploymentMode string) string {
	if checkType != CheckTypeDocker || deploymentMode != config.DeploymentModeSaaS {
		return ""
	}

	return "On this deployment, docker checks only run when pinned to one of your organization's " +
		"private locations (an agent in your own network) — shared regions reject them."
}

// CheckTypeStatus extends CheckTypeMeta with activation status.
type CheckTypeStatus struct {
	CheckTypeMeta
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabledReason,omitempty"`
	// Note is an optional advisory for an otherwise-enabled type whose
	// availability depends on something the org must still satisfy (today:
	// docker in SaaS mode requires a private-location placement). Empty when
	// there is nothing to say.
	Note string `json:"note,omitempty"`
}

// resolveServerEnabled applies the config precedence rules to determine server-enabled types.
func resolveServerEnabled(cfg *config.CheckersConfig, allMetas []CheckTypeMeta) []CheckType {
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
