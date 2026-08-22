package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrStatusPageSettingsScanType is returned when StatusPageSettings.Scan
// receives a value of an unexpected SQL type.
var ErrStatusPageSettingsScanType = errors.New("unsupported type for StatusPageSettings.Scan")

// Default availability color thresholds (spec 2026-08-03-01), used whenever a
// page's settings (or a section within it) don't specify a value. Badges
// (internal/handlers/badges) intentionally stay on these same numeric
// defaults without reading page settings — see that package's Decisions.
const (
	// DefaultAvailabilityThresholdUp is the green floor: pct >= this is "up".
	DefaultAvailabilityThresholdUp = 99.9
	// DefaultAvailabilityThresholdDegraded is the amber floor: pct >= this
	// (and below the up threshold) is "degraded"; below it is "down".
	DefaultAvailabilityThresholdDegraded = 99.0
)

// StatusPageSettings is the typed decode target for status_pages.settings
// (Postgres jsonb / SQLite text). It is the home for per-page display
// customization knobs — availability thresholds and brand identity — added
// without a two-dialect migration each time. Keep it a typed struct, not a free-form
// map, so keys stay discoverable and validation lives in one place.
type StatusPageSettings struct {
	Availability *AvailabilitySettings `json:"availability,omitempty"`
	// Branding is the page's brand identity — logo, favicon, white-label
	// opt-in (spec 2026-08-22-03). It lives here rather than in three columns
	// of its own because it is read only while RENDERING the page: nothing
	// filters, joins or uniquely constrains on it, which is the rule written
	// down in wiki/conventions/database.md.
	Branding *BrandingSettings `json:"branding,omitempty"`
}

// BrandingSettings is the page's brand identity. File UIDs, not URLs — the
// public URL is derived (statuspageassets.PublicURL), so the stored value stays
// valid when the route changes.
type BrandingSettings struct {
	// LogoFileUID is the `files.uid` of the page's own logo. nil = wear the
	// SolidPing logo.
	LogoFileUID *string `json:"logoFileUid,omitempty"`
	// FaviconFileUID is the `files.uid` of the page's own favicon. nil = the
	// default favicon.
	FaviconFileUID *string `json:"faviconFileUid,omitempty"`
	// HideBranding is the page's HALF of the white-label decision: the
	// "powered by SolidPing" footer disappears only when this is true AND the
	// org holds the `whiteLabel` entitlement. Keeping the two halves separate
	// means a downgrade silently restores the badge without rewriting the page.
	HideBranding bool `json:"hideBranding,omitempty"`
}

// LogoFileUID returns the page's logo file UID, nil when unset. Nil-safe on the
// section so no caller has to check it.
func (s StatusPageSettings) LogoFileUID() *string {
	if s.Branding == nil {
		return nil
	}

	return s.Branding.LogoFileUID
}

// FaviconFileUID returns the page's favicon file UID, nil when unset.
func (s StatusPageSettings) FaviconFileUID() *string {
	if s.Branding == nil {
		return nil
	}

	return s.Branding.FaviconFileUID
}

// HideBranding reports the page's stored white-label opt-in (false when the
// section is absent). It is only half of the decision — see BrandingSettings.
func (s StatusPageSettings) HideBranding() bool {
	return s.Branding != nil && s.Branding.HideBranding
}

// AvailabilitySettings customizes the green/amber/red availability
// thresholds rendered on a status page's bars. Nil fields fall back to the
// package defaults (DefaultAvailabilityThresholdUp/Degraded).
type AvailabilitySettings struct {
	// ThresholdUp is the green floor (pct >= this renders "up"). nil = 99.9.
	ThresholdUp *float64 `json:"thresholdUp,omitempty"`
	// ThresholdDegraded is the amber floor (pct >= this and < ThresholdUp
	// renders "degraded"; below it renders "down", subject to the
	// small-bucket calibration guard). nil = 99.0.
	ThresholdDegraded *float64 `json:"thresholdDegraded,omitempty"`
}

// EffectiveThresholds resolves the up/degraded thresholds for a (possibly
// nil) AvailabilitySettings, falling back to the package defaults for a nil
// receiver or nil fields. Never returns a value that needs further nil
// checking by the caller.
func (a *AvailabilitySettings) EffectiveThresholds() (float64, float64) {
	thresholdUp := DefaultAvailabilityThresholdUp
	thresholdDegraded := DefaultAvailabilityThresholdDegraded

	if a == nil {
		return thresholdUp, thresholdDegraded
	}

	if a.ThresholdUp != nil {
		thresholdUp = *a.ThresholdUp
	}

	if a.ThresholdDegraded != nil {
		thresholdDegraded = *a.ThresholdDegraded
	}

	return thresholdUp, thresholdDegraded
}

// EffectiveThresholds resolves the page's effective availability thresholds,
// nil-safe on the Availability section (StatusPageSettings itself is always a
// value, never nil, since it is stored NOT NULL DEFAULT '{}').
func (s StatusPageSettings) EffectiveThresholds() (float64, float64) {
	return s.Availability.EffectiveThresholds()
}

// Value implements driver.Valuer so StatusPageSettings persists as a JSON
// string on both Postgres (jsonb) and SQLite (text). The zero value marshals
// to "{}", matching the column's NOT NULL DEFAULT '{}'.
func (s StatusPageSettings) Value() (driver.Value, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal StatusPageSettings: %w", err)
	}

	return string(data), nil
}

// Scan implements sql.Scanner for reading the JSON blob back from either
// engine.
func (s *StatusPageSettings) Scan(value any) error {
	if value == nil {
		return nil
	}

	var data []byte

	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("%w: %T", ErrStatusPageSettingsScanType, value)
	}

	if len(data) == 0 {
		return nil
	}

	return json.Unmarshal(data, s)
}
