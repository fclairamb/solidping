package entitlements

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Errors returned by the entitlements service.
var (
	// ErrEntitlementExceeded is returned by CanCreate when the requested
	// resource would exceed the org's quota.
	ErrEntitlementExceeded = errors.New("entitlement exceeded")
	// ErrFeatureNotEntitled is returned by FeatureEnabled when a boolean
	// feature is off for the org.
	ErrFeatureNotEntitled = errors.New("feature not entitled")
	// ErrUnknownResource is returned when CanCreate is called with a name
	// the service doesn't know how to count.
	ErrUnknownResource = errors.New("unknown resource for entitlement check")
	// ErrUnknownFeature is returned when FeatureEnabled gets a name it
	// doesn't recognize.
	ErrUnknownFeature = errors.New("unknown feature")
)

// QuotaError carries the precise numbers a frontend needs to render a
// "you're at 5/5 checks" message. Returned from CanCreate.
type QuotaError struct {
	LimitName    string
	Limit        int
	CurrentUsage int
}

// Error implements error.
func (e *QuotaError) Error() string {
	return fmt.Sprintf(
		"entitlement exceeded: %s limit is %d, current usage is %d",
		e.LimitName, e.Limit, e.CurrentUsage,
	)
}

// Unwrap allows errors.Is(err, ErrEntitlementExceeded) to match.
func (e *QuotaError) Unwrap() error { return ErrEntitlementExceeded }

// Service provides entitlement Resolve / Set / CanCreate / FeatureEnabled.
// In PR 1, CanCreate / FeatureEnabled are stubs that always allow — the
// enforcement-batch PRs wire them into actual handlers.
type Service struct {
	db         db.Service
	defaults   Entitlements
	staleAfter time.Duration
	now        func() time.Time
}

// NewService builds an entitlements service with the given defaults.
// staleAfter of zero disables stale fallback (self-hosted default).
//
// defaults is held by value for the service's lifetime; passing by
// pointer would invite mutation.
//
//nolint:gocritic
func NewService(
	dbService db.Service, defaults Entitlements, staleAfter time.Duration,
) *Service {
	return &Service{
		db:         dbService,
		defaults:   defaults,
		staleAfter: staleAfter,
		now:        time.Now,
	}
}

// Resolve merges the stored row with defaults and live usage. Falls back
// to defaults entirely when the row is stale (last_synced_at older than
// staleAfter and source == billing-service); admin overrides do not go
// stale.
func (s *Service) Resolve(ctx context.Context, orgUID string) (Resolved, error) {
	row, err := s.db.GetOrgEntitlements(ctx, orgUID)
	if err != nil {
		return Resolved{}, fmt.Errorf("get org entitlements: %w", err)
	}

	stale := s.isStale(row)
	merged := s.merge(row, stale)

	usage, err := s.computeUsage(ctx, orgUID)
	if err != nil {
		return Resolved{}, fmt.Errorf("compute usage: %w", err)
	}

	merged.Usage = usage
	merged.Stale = stale

	return merged, nil
}

// Set replaces the entitlement row and writes an audit entry in the same
// transaction. Pass empty actor for unattended writes.
//
//nolint:gocritic // in is the request payload, intentional value semantics
func (s *Service) Set(
	ctx context.Context, orgUID string, input Entitlements, actor, reason string,
) error {
	previous, err := s.db.GetOrgEntitlements(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("load previous entitlements: %w", err)
	}

	row := toModel(orgUID, input, previous)

	var beforeJSON models.JSONMap
	if previous != nil {
		beforeJSON, err = modelToJSON(previous)
		if err != nil {
			return fmt.Errorf("snapshot before: %w", err)
		}
	}

	afterJSON, err := modelToJSON(row)
	if err != nil {
		return fmt.Errorf("snapshot after: %w", err)
	}

	var reasonPtr *string
	if reason != "" {
		r := reason
		reasonPtr = &r
	}

	audit := models.NewOrgEntitlementAudit(
		orgUID, string(input.Source), actor, beforeJSON, afterJSON, reasonPtr,
	)

	if err := s.db.UpsertOrgEntitlements(ctx, row, audit); err != nil {
		return fmt.Errorf("upsert entitlements: %w", err)
	}

	slog.InfoContext(ctx, "entitlements written",
		"orgUID", orgUID, "source", input.Source, "actor", actor)

	return nil
}

// CanCreate is the boundary check for create-* handlers. PR 1 stub: no
// enforcement is wired in yet, so this always returns nil. Enforcement
// PRs (3 and 4) add actual count vs limit logic.
func (s *Service) CanCreate(_ context.Context, _ /*orgUID*/, _ /*resource*/ string) error {
	return nil
}

// FeatureEnabled is the boolean-feature gate for handlers. PR 1 stub:
// always returns the default value for the feature so callers wired in
// the early enforcement PRs see no behavior change.
func (s *Service) FeatureEnabled(_ context.Context, _ /*orgUID*/, feature string) (bool, error) {
	val, ok := defaultFeatureFlag(s.defaults.Features, feature)
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrUnknownFeature, feature)
	}

	return val, nil
}

// Defaults returns the in-memory defaults for callers that need to render
// "X / Y" indicators when no row exists.
func (s *Service) Defaults() Entitlements {
	return s.defaults
}

// merge produces a Resolved by filling nulls from defaults. If stale is
// true, the entire row is dropped in favor of defaults (caller still
// surfaces row.Source and DisplayName so the UI can label the stale
// state). Straight-line per-field overlay; a loop would obscure the
// type-level mapping.
//
//nolint:cyclop,funlen
func (s *Service) merge(row *models.OrgEntitlements, stale bool) Resolved {
	out := Resolved{
		Limits:   s.defaults.Limits,
		Features: s.defaults.Features,
		Source:   models.EntitlementSourceDefault,
	}

	if row == nil || stale {
		return out
	}

	out.Source = row.Source
	out.DisplayName = row.DisplayName
	out.ExpiresAt = row.ExpiresAt
	out.LastSyncedAt = row.LastSyncedAt

	if row.MaxChecks != nil {
		out.Limits.MaxChecks = row.MaxChecks
	}
	if row.MaxMembers != nil {
		out.Limits.MaxMembers = row.MaxMembers
	}
	if row.MaxStatusPages != nil {
		out.Limits.MaxStatusPages = row.MaxStatusPages
	}
	if row.MaxCheckGroups != nil {
		out.Limits.MaxCheckGroups = row.MaxCheckGroups
	}
	if row.MaxMaintenanceWindows != nil {
		out.Limits.MaxMaintenanceWindows = row.MaxMaintenanceWindows
	}
	if row.MaxConnections != nil {
		out.Limits.MaxConnections = row.MaxConnections
	}
	if row.MaxWorkers != nil {
		out.Limits.MaxWorkers = row.MaxWorkers
	}
	if row.MaxAPITokens != nil {
		out.Limits.MaxAPITokens = row.MaxAPITokens
	}
	if row.RetentionDaysRaw != nil {
		out.Limits.RetentionDaysRaw = row.RetentionDaysRaw
	}
	if row.RetentionDaysAggregated != nil {
		out.Limits.RetentionDaysAggregated = row.RetentionDaysAggregated
	}
	if row.MinCheckPeriodSeconds != nil {
		out.Limits.MinCheckPeriodSeconds = row.MinCheckPeriodSeconds
	}
	if row.FeatureSSO != nil {
		out.Features.SSO = row.FeatureSSO
	}
	if row.FeatureMCP != nil {
		out.Features.MCP = row.FeatureMCP
	}
	if row.FeatureCustomBranding != nil {
		out.Features.CustomBranding = row.FeatureCustomBranding
	}
	if row.FeaturePrioritySupport != nil {
		out.Features.PrioritySupport = row.FeaturePrioritySupport
	}
	if row.FeatureMultiRegion != nil {
		out.Features.MultiRegion = row.FeatureMultiRegion
	}
	if row.FeatureAdvancedAlerts != nil {
		out.Features.AdvancedAlerts = row.FeatureAdvancedAlerts
	}
	if row.AllowedCheckTypes != nil && *row.AllowedCheckTypes != "" {
		var types []string
		if err := json.Unmarshal([]byte(*row.AllowedCheckTypes), &types); err == nil {
			out.AllowedCheckTypes = types
		}
	}

	return out
}

// isStale returns true if the row is past its sync window. Only billing-
// service rows can go stale; admin overrides are deliberate and persist.
func (s *Service) isStale(row *models.OrgEntitlements) bool {
	if row == nil {
		return false
	}
	if s.staleAfter <= 0 {
		return false
	}
	if row.Source != models.EntitlementSourceBilling {
		return false
	}
	if row.LastSyncedAt == nil {
		return false
	}

	return s.now().Sub(*row.LastSyncedAt) > s.staleAfter
}

// computeUsage reads counts from the db for the org-scoped resources
// most useful to render a "X / Y used" panel. Cheap when there is no
// quota set (the queries are bounded by org_uid).
func (s *Service) computeUsage(ctx context.Context, orgUID string) (Usage, error) {
	checks, err := s.db.CountChecksForOrg(ctx, orgUID)
	if err != nil {
		return Usage{}, err
	}
	members, err := s.db.ListMembersByOrg(ctx, orgUID)
	if err != nil {
		return Usage{}, err
	}
	statusPages, err := s.db.CountStatusPagesForOrg(ctx, orgUID)
	if err != nil {
		return Usage{}, err
	}
	checkGroups, err := s.db.CountCheckGroupsForOrg(ctx, orgUID)
	if err != nil {
		return Usage{}, err
	}
	connections, err := s.db.CountConnectionsForOrg(ctx, orgUID)
	if err != nil {
		return Usage{}, err
	}

	return Usage{
		Checks:      checks,
		Members:     len(members),
		StatusPages: statusPages,
		CheckGroups: checkGroups,
		Connections: connections,
	}, nil
}

// toModel maps an Entitlements (input) onto an OrgEntitlements row. If
// previous is non-nil the existing UID is preserved; otherwise a fresh
// one is generated.
//
//nolint:gocritic // input is the inbound payload — value semantics intentional
func toModel(
	orgUID string, input Entitlements, previous *models.OrgEntitlements,
) *models.OrgEntitlements {
	row := models.NewOrgEntitlements(orgUID, input.Source)
	if previous != nil {
		row.UID = previous.UID
		row.CreatedAt = previous.CreatedAt
	}

	row.MaxChecks = input.Limits.MaxChecks
	row.MaxMembers = input.Limits.MaxMembers
	row.MaxStatusPages = input.Limits.MaxStatusPages
	row.MaxCheckGroups = input.Limits.MaxCheckGroups
	row.MaxMaintenanceWindows = input.Limits.MaxMaintenanceWindows
	row.MaxConnections = input.Limits.MaxConnections
	row.MaxWorkers = input.Limits.MaxWorkers
	row.MaxAPITokens = input.Limits.MaxAPITokens
	row.RetentionDaysRaw = input.Limits.RetentionDaysRaw
	row.RetentionDaysAggregated = input.Limits.RetentionDaysAggregated
	row.MinCheckPeriodSeconds = input.Limits.MinCheckPeriodSeconds

	row.FeatureSSO = input.Features.SSO
	row.FeatureMCP = input.Features.MCP
	row.FeatureCustomBranding = input.Features.CustomBranding
	row.FeaturePrioritySupport = input.Features.PrioritySupport
	row.FeatureMultiRegion = input.Features.MultiRegion
	row.FeatureAdvancedAlerts = input.Features.AdvancedAlerts

	if len(input.AllowedCheckTypes) > 0 {
		raw, err := json.Marshal(input.AllowedCheckTypes)
		if err == nil {
			s := string(raw)
			row.AllowedCheckTypes = &s
		}
	}

	row.DisplayName = input.DisplayName
	row.ExternalRef = input.ExternalRef
	if input.Metadata != nil {
		row.Metadata = models.JSONMap(input.Metadata)
	}
	row.ExpiresAt = input.ExpiresAt
	row.LastSyncedAt = input.LastSyncedAt

	return row
}

// modelToJSON serializes an OrgEntitlements into a JSON snapshot for the
// audit log.
//
//nolint:musttag // OrgEntitlements is bun-tagged; default JSON encoding is acceptable for the audit blob
func modelToJSON(row *models.OrgEntitlements) (models.JSONMap, error) {
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}

	var out models.JSONMap
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// defaultFeatureFlag looks up a feature by name. Used by FeatureEnabled
// in PR 1 (pre-enforcement) to surface the default value.
func defaultFeatureFlag(features Features, name string) (bool, bool) {
	switch name {
	case "sso":
		if features.SSO != nil {
			return *features.SSO, true
		}
	case "mcp":
		if features.MCP != nil {
			return *features.MCP, true
		}
	case "custom_branding":
		if features.CustomBranding != nil {
			return *features.CustomBranding, true
		}
	case "priority_support":
		if features.PrioritySupport != nil {
			return *features.PrioritySupport, true
		}
	case "multi_region":
		if features.MultiRegion != nil {
			return *features.MultiRegion, true
		}
	case "advanced_alerts":
		if features.AdvancedAlerts != nil {
			return *features.AdvancedAlerts, true
		}
	default:
		return false, false
	}

	return false, false
}
