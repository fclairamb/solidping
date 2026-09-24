// Package regionoutage holds the persisted state of the per-minute region
// sweep (spec 2026-09-25-03): one global state entry per cloud region that is
// currently dark or stalled.
//
// It is a leaf on purpose. Three packages read it and must not import each
// other: the sweep that writes it (internal/regionsweep), the hourly watchdog
// that must not "resolve" an outage the sweep still owns (internal/watchdog),
// and the org regions endpoint that tells dash0 which region is offline
// (internal/handlers/regions).
package regionoutage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// KeyPrefix namespaces the markers. They are GLOBAL state entries
// (organization_uid IS NULL): a cloud region is shared by every org, and
// ListStateEntries cannot list one prefix across orgs, so the orgs that were
// told about an outage live inside the marker instead of in org-scoped rows.
const KeyPrefix = "region-outage:"

// TTL bounds how long a marker survives without the sweep refreshing it. The
// sweep rewrites a marker on every run while its region is unhealthy, so this
// only expires on an instance whose sweep stopped for a month.
const TTL = 30 * 24 * time.Hour

// Phase is what the sweep currently believes about a region.
type Phase string

// Phases. A healthy region has no marker at all.
const (
	// PhaseDark is a region with assigned jobs and no live worker: nothing
	// can run its checks.
	PhaseDark Phase = "dark"
	// PhaseStalled is a region whose workers are alive but not claiming: its
	// jobs are overdue past max(2 × period, 5 min). Operator-only.
	PhaseStalled Phase = "stalled"
)

// Marker field names.
const (
	fieldRegion        = "region"
	fieldPhase         = "phase"
	fieldSince         = "since"
	fieldDetectedAt    = "detectedAt"
	fieldLastSeenAt    = "lastSeenAt"
	fieldHealthyStreak = "healthyStreak"
	fieldNotifiedOrgs  = "notifiedOrgs"
)

// Marker is one region's outage state.
type Marker struct {
	Region string
	Phase  Phase
	// Since is when the outage started from the region's point of view: the
	// last time a worker of the region was seen (for dark), not the time the
	// sweep noticed.
	Since time.Time
	// DetectedAt is when the sweep first recorded the outage.
	DetectedAt time.Time
	// LastSeenAt is the last sweep that observed the region unhealthy.
	LastSeenAt time.Time
	// HealthyStreak counts consecutive healthy sweeps. Recovery needs two.
	HealthyStreak int
	// NotifiedOrgs are the org UIDs that received the offline notice, and so
	// exactly the orgs the recovery notice goes to.
	NotifiedOrgs []string
}

// IsDark reports whether the marker says the region is offline.
func (m *Marker) IsDark() bool {
	return m != nil && m.Phase == PhaseDark
}

// HasNotified reports whether an org already received the offline notice.
func (m *Marker) HasNotified(orgUID string) bool {
	for _, uid := range m.NotifiedOrgs {
		if uid == orgUID {
			return true
		}
	}

	return false
}

// Key is the state-entry key of one region's marker.
func Key(region string) string {
	return KeyPrefix + region
}

// Load reads one region's marker, nil when the region has none.
func Load(ctx context.Context, dbService db.Service, region string) (*Marker, error) {
	entry, err := dbService.GetStateEntry(ctx, nil, Key(region))
	if err != nil {
		return nil, fmt.Errorf("get region outage marker %s: %w", region, err)
	}

	if entry == nil {
		return nil, nil //nolint:nilnil // no marker means the region is healthy
	}

	return decode(entry), nil
}

// List reads every marker, keyed by region slug.
func List(ctx context.Context, dbService db.Service) (map[string]*Marker, error) {
	entries, err := dbService.ListStateEntries(ctx, nil, KeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("list region outage markers: %w", err)
	}

	out := make(map[string]*Marker, len(entries))

	for _, entry := range entries {
		marker := decode(entry)
		out[marker.Region] = marker
	}

	return out, nil
}

// Save writes one region's marker, refreshing its TTL.
func Save(ctx context.Context, dbService db.Service, marker *Marker) error {
	orgs := append([]string(nil), marker.NotifiedOrgs...)
	sort.Strings(orgs)

	orgValues := make([]any, 0, len(orgs))
	for _, uid := range orgs {
		orgValues = append(orgValues, uid)
	}

	value := models.JSONMap{
		fieldRegion:        marker.Region,
		fieldPhase:         string(marker.Phase),
		fieldSince:         marker.Since.UTC().Format(time.RFC3339Nano),
		fieldDetectedAt:    marker.DetectedAt.UTC().Format(time.RFC3339Nano),
		fieldLastSeenAt:    marker.LastSeenAt.UTC().Format(time.RFC3339Nano),
		fieldHealthyStreak: marker.HealthyStreak,
		fieldNotifiedOrgs:  orgValues,
	}

	ttl := TTL
	if err := dbService.SetStateEntry(ctx, nil, Key(marker.Region), &value, &ttl); err != nil {
		return fmt.Errorf("write region outage marker %s: %w", marker.Region, err)
	}

	return nil
}

// Delete clears one region's marker.
func Delete(ctx context.Context, dbService db.Service, region string) error {
	if _, err := dbService.DeleteStateEntry(ctx, nil, Key(region)); err != nil {
		return fmt.Errorf("clear region outage marker %s: %w", region, err)
	}

	return nil
}

// decode reads a marker off a state entry. The region falls back to the key
// suffix, so a marker whose value was hand-edited still maps to its region.
func decode(entry *models.StateEntry) *Marker {
	marker := &Marker{Region: entry.Key[len(KeyPrefix):]}

	if entry.Value == nil {
		return marker
	}

	value := *entry.Value

	if region, ok := value[fieldRegion].(string); ok && region != "" {
		marker.Region = region
	}

	if phase, ok := value[fieldPhase].(string); ok {
		marker.Phase = Phase(phase)
	}

	marker.Since = readTime(value, fieldSince)
	marker.DetectedAt = readTime(value, fieldDetectedAt)
	marker.LastSeenAt = readTime(value, fieldLastSeenAt)

	switch streak := value[fieldHealthyStreak].(type) {
	case float64:
		marker.HealthyStreak = int(streak)
	case int:
		marker.HealthyStreak = streak
	case int64:
		marker.HealthyStreak = int(streak)
	}

	if orgs, ok := value[fieldNotifiedOrgs].([]any); ok {
		for _, raw := range orgs {
			if uid, isString := raw.(string); isString && uid != "" {
				marker.NotifiedOrgs = append(marker.NotifiedOrgs, uid)
			}
		}
	}

	return marker
}

// readTime parses one RFC3339 field, zero when absent or unparseable.
func readTime(value models.JSONMap, field string) time.Time {
	raw, ok := value[field].(string)
	if !ok || raw == "" {
		return time.Time{}
	}

	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}

	return parsed
}
