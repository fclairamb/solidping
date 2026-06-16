// Package entitlements owns per-org limits.
//
// The OSS knows nothing about plan SKUs, prices, trials, or invoices —
// those live in a separate billing service. This package stores raw
// numbers, exposes them via HTTP, and enforces them at the SSO callback
// (MaxSSOUsers) and at the worker dispatch (MaxChecksPerMinute).
//
// nil = unlimited. Defaults vary by deployment mode; everything else is
// unbounded.
package entitlements

import (
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Limits aliases the model-layer struct so the public API is stable
// across the JSONB-collapse refactor. nil = unlimited.
type Limits = models.EntitlementLimits

// Entitlements is the full input/output shape used by the API and the
// audit log. On disk, Limits + DisplayName live inside the row's
// `payload` JSONB column; Source / ExternalRef / Metadata / ExpiresAt /
// LastSyncedAt are break-out columns. The wire format preserves that.
type Entitlements struct {
	Limits       Limits                   `json:"limits"`
	Source       models.EntitlementSource `json:"source"`
	DisplayName  *string                  `json:"displayName,omitempty"`
	DisplayEmoji *string                  `json:"displayEmoji,omitempty"`
	ExternalRef  *string                  `json:"externalRef,omitempty"`
	Metadata     map[string]any           `json:"metadata,omitempty"`
	ExpiresAt    *time.Time               `json:"expiresAt,omitempty"`
	LastSyncedAt *time.Time               `json:"lastSyncedAt,omitempty"`
}

// Resolved is the resolver output: defaults merged with the stored row,
// plus the stale flag. NULL fields in the row are replaced by the
// corresponding default. Returned to the API.
type Resolved struct {
	Limits       Limits                   `json:"limits"`
	Source       models.EntitlementSource `json:"source"`
	DisplayName  *string                  `json:"displayName,omitempty"`
	DisplayEmoji *string                  `json:"displayEmoji,omitempty"`
	ExpiresAt    *time.Time               `json:"expiresAt,omitempty"`
	LastSyncedAt *time.Time               `json:"lastSyncedAt,omitempty"`
	Stale        bool                     `json:"stale"`
}

// Int is a tiny helper for default-defining int pointers.
func Int(i int) *int { return &i }

// Default cap values per deployment mode.
const (
	defaultMaxSSOUsersSelfHosted  = 30
	defaultMaxChecksPerMinuteSaaS = 6
)

// DefaultsFor returns the in-memory seed for a fresh org under the
// given deployment mode. SaaS caps aggregate check rate; self-hosted
// caps SSO seats. Anything else is nil = unlimited.
//
// Unknown modes log a warning and fall back to self-hosted defaults
// rather than booting unbounded — startup validation should have caught
// the typo upstream, but the safer side is to keep limits in place.
func DefaultsFor(mode string) Entitlements {
	switch mode {
	case config.DeploymentModeSaaS:
		return Entitlements{
			Limits: Limits{
				MaxChecksPerMinute: Int(defaultMaxChecksPerMinuteSaaS),
			},
			Source: models.EntitlementSourceDefault,
		}
	case config.DeploymentModeSelfHosted:
		return Entitlements{
			Limits: Limits{
				MaxSSOUsers: Int(defaultMaxSSOUsersSelfHosted),
			},
			Source: models.EntitlementSourceDefault,
		}
	default:
		slog.Warn("unknown deployment mode; falling back to self-hosted entitlement defaults",
			"mode", mode)

		return Entitlements{
			Limits: Limits{
				MaxSSOUsers: Int(defaultMaxSSOUsersSelfHosted),
			},
			Source: models.EntitlementSourceDefault,
		}
	}
}
