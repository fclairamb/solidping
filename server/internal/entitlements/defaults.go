// Package entitlements owns per-org limits.
//
// The OSS knows nothing about plan SKUs, prices, trials, or invoices —
// those live in a separate billing service. This package stores raw
// numbers, exposes them via HTTP, and enforces them at every membership-
// creation path (MaxUsers) and at the worker dispatch (MaxChecksPerMinute).
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
//
// SaaS defaults MUST stay in sync with solidping-billing's Free SKU
// (solidping-billing/server/internal/plans/plans.go) — this is the
// "billing service never reconciled us yet" fallback for a fresh SaaS
// org, and it must render/enforce identically to the real Free plan
// until billing writes its own org_entitlements row.
// The SaaS numbers implement the Free tier of the pricing decision of
// 2026-07-12 (Free €0: 100 checks, 5 seats), with the check rate raised
// from 6 to 10 checks/min on 2026-08-15: at 6 the free tier was the
// tightest in the segment (exit1.dev free is 50 monitors at 5 min = 10,
// UptimeRobot the same), which cost us top-of-funnel signups.
const (
	defaultMaxUsersSelfHosted     = 30
	defaultMaxChecksSaaS          = 100
	defaultMaxChecksPerMinuteSaaS = 10
	defaultMaxUsersSaaS           = 5
	// defaultMaxDeportedAgentsSaaS mirrors the Free SKU's private-location
	// agent cap (plan ladder: Free 1, Starter 3, Pro 6, Scale 9 — see
	// wiki/features/deported-agents.md). Self-hosted stays unlimited
	// (nil) — no const needed, the field is simply left unset below.
	defaultMaxDeportedAgentsSaaS = 1
	// defaultMaxCustomDomainsSaaS is 0: the Free plan ships no custom domains;
	// billing raises it per paid plan. Self-hosted stays unlimited (nil).
	defaultMaxCustomDomainsSaaS = 0
	// defaultMaxSmsPerMonthSaaS / defaultMaxCallsPerMonthSaaS are 0: the Free
	// plan ships no SMS/voice; billing raises them per paid plan. Self-hosted
	// stays unlimited (nil) — bring-your-own Twilio.
	defaultMaxSmsPerMonthSaaS   = 0
	defaultMaxCallsPerMonthSaaS = 0
	// defaultMaxWhatsappPerMonthSaaS is 0 for the same reason: the Free plan
	// ships no WhatsApp alerts. Self-hosted stays unlimited (nil) — the
	// operator pays Meta directly for their own WABA.
	defaultMaxWhatsappPerMonthSaaS = 0
)

// Display identity shown on the usage page when a row has none of its
// own (see Service.merge). SaaS mirrors billing's Free plan identity;
// self-hosted gets a plain label so it never claims to be "Free".
const (
	displayNameSaaS  = "Free"
	displayEmojiSaaS = "🆓"

	displayNameSelfHosted  = "Self-hosted"
	displayEmojiSelfHosted = "🏠"
)

// strPtr is a tiny helper for default-defining string pointers, mirroring Int.
func strPtr(s string) *string { return &s }

// DefaultsFor returns the in-memory seed for a fresh org under the
// given deployment mode. SaaS caps mirror the billing Free plan
// (checks + aggregate check rate); self-hosted caps total members.
// Anything else is nil = unlimited.
//
// Unknown modes log a warning and fall back to self-hosted defaults
// rather than booting unbounded — startup validation should have caught
// the typo upstream, but the safer side is to keep limits in place.
func DefaultsFor(mode string) Entitlements {
	switch mode {
	case config.DeploymentModeSaaS:
		return Entitlements{
			Limits: Limits{
				MaxChecks:           Int(defaultMaxChecksSaaS),
				MaxChecksPerMinute:  Int(defaultMaxChecksPerMinuteSaaS),
				MaxUsers:            Int(defaultMaxUsersSaaS),
				MaxDeportedAgents:   Int(defaultMaxDeportedAgentsSaaS),
				MaxCustomDomains:    Int(defaultMaxCustomDomainsSaaS),
				MaxSmsPerMonth:      Int(defaultMaxSmsPerMonthSaaS),
				MaxCallsPerMonth:    Int(defaultMaxCallsPerMonthSaaS),
				MaxWhatsappPerMonth: Int(defaultMaxWhatsappPerMonthSaaS),
			},
			Source:       models.EntitlementSourceDefault,
			DisplayName:  strPtr(displayNameSaaS),
			DisplayEmoji: strPtr(displayEmojiSaaS),
		}
	case config.DeploymentModeSelfHosted:
		return Entitlements{
			Limits: Limits{
				MaxUsers: Int(defaultMaxUsersSelfHosted),
				// MaxDeportedAgents stays nil (unlimited) — self-hosted keeps
				// the "free private locations" competitive positioning
				// documented in wiki/features/deported-agents.md.
			},
			Source:       models.EntitlementSourceDefault,
			DisplayName:  strPtr(displayNameSelfHosted),
			DisplayEmoji: strPtr(displayEmojiSelfHosted),
		}
	default:
		slog.Warn("unknown deployment mode; falling back to self-hosted entitlement defaults",
			"mode", mode)

		return Entitlements{
			Limits: Limits{
				MaxUsers: Int(defaultMaxUsersSelfHosted),
			},
			Source:       models.EntitlementSourceDefault,
			DisplayName:  strPtr(displayNameSelfHosted),
			DisplayEmoji: strPtr(displayEmojiSelfHosted),
		}
	}
}
