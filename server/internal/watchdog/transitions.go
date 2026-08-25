package watchdog

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// StateKeyPrefix namespaces the per-anomaly anti-flood markers. They are
// GLOBAL state entries (organization_uid IS NULL) because the watchdog reports
// on the platform, not on a tenant.
const StateKeyPrefix = "watchdog:anomaly:"

// stateTTL bounds how long a marker survives without the watchdog running.
// Every run refreshes it, so in practice it only expires on an instance where
// the watchdog has been off for a month — at which point treating whatever
// comes back as "new" is exactly right.
const stateTTL = 30 * 24 * time.Hour

// State-entry value keys.
const (
	stateFieldFirstSeen      = "firstSeenAt"
	stateFieldLastSeen       = "lastSeenAt"
	stateFieldLastNotified   = "lastNotifiedAt"
	stateFieldSeverity       = "severity"
	stateFieldHeadline       = "headline"
	stateFieldNotifiedSevRaw = "notifiedSeverity"
)

// TransitionKind is what happened to one anomaly between the previous run and
// this one. The watchdog reports TRANSITIONS, never state: an anomaly that
// persists must not re-page every hour, and a resolution must be announced
// because "watchdog sees 0 stranded jobs" is the operator's exit criterion.
type TransitionKind string

// Transition kinds.
const (
	// TransitionNew is a fingerprint seen for the first time — always notifies.
	TransitionNew TransitionKind = "new"
	// TransitionOngoing is a known fingerprint inside its re-notify window —
	// recorded, never notified.
	TransitionOngoing TransitionKind = "ongoing"
	// TransitionRenotify is a known fingerprint past the re-notify window (or
	// whose severity has escalated) — notifies as "still broken since …".
	TransitionRenotify TransitionKind = "renotify"
	// TransitionResolved is a fingerprint that was present and is now gone —
	// notifies exactly once, then the marker is cleared.
	TransitionResolved TransitionKind = "resolved"
)

// Transition is one anomaly's movement across this run.
type Transition struct {
	Fingerprint string
	Kind        TransitionKind
	// Anomaly is the current anomaly. Zero-valued for TransitionResolved,
	// which by definition has no current anomaly — read Fingerprint and
	// FirstSeenAt instead.
	Anomaly     Anomaly
	FirstSeenAt time.Time
	// Notify reports whether this transition belongs in the digest.
	Notify bool
}

// NotifiableTransitions filters the transitions that belong in a digest.
func NotifiableTransitions(transitions []Transition) []Transition {
	out := make([]Transition, 0, len(transitions))

	for i := range transitions {
		if transitions[i].Notify {
			out = append(out, transitions[i])
		}
	}

	return out
}

// Reconcile diffs this run's anomalies against the persisted fingerprints,
// writes the updated markers, and returns what moved.
//
// The load-bearing rule: a fingerprint is only considered RESOLVED when the
// detector that would have produced it actually ran successfully. A query
// error must never be laundered into a false "recovered" notice — that would
// tell an operator the outage is over while it is still going.
func (s *Service) Reconcile(ctx context.Context, report *Report, cfg *Config) ([]Transition, error) {
	now := s.now()

	entries, err := s.db.ListStateEntries(ctx, nil, StateKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("list watchdog state entries: %w", err)
	}

	known := make(map[string]*models.StateEntry, len(entries))
	for _, entry := range entries {
		known[entry.Key[len(StateKeyPrefix):]] = entry
	}

	transitions := make([]Transition, 0, len(report.Anomalies)+len(known))
	current := make(map[string]bool, len(report.Anomalies))

	for i := range report.Anomalies {
		anomaly := &report.Anomalies[i]
		fingerprint := anomaly.Fingerprint()
		current[fingerprint] = true

		previous := known[fingerprint]
		transition := classify(anomaly, previous, cfg, now)

		if markerErr := s.writeMarker(ctx, fingerprint, &transition, previous, now); markerErr != nil {
			return nil, markerErr
		}

		transitions = append(transitions, transition)
	}

	resolved, err := s.reconcileResolved(ctx, report, known, current, now)
	if err != nil {
		return nil, err
	}

	transitions = append(transitions, resolved...)

	sort.SliceStable(transitions, func(i, j int) bool {
		return transitions[i].Fingerprint < transitions[j].Fingerprint
	})

	return transitions, nil
}

// reconcileResolved turns every stored fingerprint the current run did NOT
// re-observe into a one-shot recovery notice — but only for detectors that
// succeeded.
func (s *Service) reconcileResolved(
	ctx context.Context, report *Report, known map[string]*models.StateEntry,
	current map[string]bool, now time.Time,
) ([]Transition, error) {
	out := make([]Transition, 0, len(known))

	for fingerprint, entry := range known {
		if current[fingerprint] {
			continue
		}

		if !report.DetectorSucceeded(detectorOf(fingerprint)) {
			// Its detector broke this run; we have no evidence the condition
			// is gone. Leave the marker exactly where it is.
			continue
		}

		if _, err := s.db.DeleteStateEntry(ctx, nil, StateKeyPrefix+fingerprint); err != nil {
			return nil, fmt.Errorf("clear watchdog state entry %s: %w", fingerprint, err)
		}

		out = append(out, Transition{
			Fingerprint: fingerprint,
			Kind:        TransitionResolved,
			FirstSeenAt: readTime(entry, stateFieldFirstSeen, now),
			Notify:      true,
		})
	}

	return out, nil
}

// detectorOf extracts the detector half of a `<detector>:<subject>`
// fingerprint.
func detectorOf(fingerprint string) string {
	for i := 0; i < len(fingerprint); i++ {
		if fingerprint[i] == ':' {
			return fingerprint[:i]
		}
	}

	return fingerprint
}

// classify decides what one anomaly's transition is against its stored marker.
func classify(anomaly *Anomaly, entry *models.StateEntry, cfg *Config, now time.Time) Transition {
	if entry == nil {
		return Transition{
			Fingerprint: anomaly.Fingerprint(),
			Kind:        TransitionNew,
			Anomaly:     *anomaly,
			FirstSeenAt: now,
			Notify:      true,
		}
	}

	firstSeen := readTime(entry, stateFieldFirstSeen, now)
	lastNotified := readTime(entry, stateFieldLastNotified, firstSeen)

	transition := Transition{
		Fingerprint: anomaly.Fingerprint(),
		Kind:        TransitionOngoing,
		Anomaly:     *anomaly,
		FirstSeenAt: firstSeen,
	}

	// An escalation is news even inside the quiet window: "still 5 jobs late"
	// and "now 400 jobs late, region dark" are not the same page.
	escalated := anomaly.Severity > ParseSeverity(readString(entry, stateFieldNotifiedSevRaw))

	if escalated || now.Sub(lastNotified) >= cfg.RenotifyAfter() {
		transition.Kind = TransitionRenotify
		transition.Notify = true
	}

	return transition
}

// writeMarker persists the anomaly's marker. lastNotifiedAt only advances on a
// transition that actually notified — otherwise the quiet window would reset
// on every run and the re-notify would never fire.
func (s *Service) writeMarker(
	ctx context.Context, fingerprint string, transition *Transition,
	previous *models.StateEntry, now time.Time,
) error {
	value := models.JSONMap{
		stateFieldFirstSeen: transition.FirstSeenAt.UTC().Format(time.RFC3339Nano),
		stateFieldLastSeen:  now.UTC().Format(time.RFC3339Nano),
		stateFieldSeverity:  transition.Anomaly.Severity.String(),
		stateFieldHeadline:  transition.Anomaly.Headline,
	}

	if transition.Notify {
		value[stateFieldLastNotified] = now.UTC().Format(time.RFC3339Nano)
		value[stateFieldNotifiedSevRaw] = transition.Anomaly.Severity.String()
	} else {
		// Carry the previous notification stamps forward untouched: advancing
		// them on a silent run would reset the quiet window every hour and the
		// 24h re-notify would never fire.
		if raw := readString(previous, stateFieldLastNotified); raw != "" {
			value[stateFieldLastNotified] = raw
		}

		if raw := readString(previous, stateFieldNotifiedSevRaw); raw != "" {
			value[stateFieldNotifiedSevRaw] = raw
		}
	}

	ttl := stateTTL
	if err := s.db.SetStateEntry(ctx, nil, StateKeyPrefix+fingerprint, &value, &ttl); err != nil {
		return fmt.Errorf("write watchdog state entry %s: %w", fingerprint, err)
	}

	return nil
}

// readString pulls a string field off a state entry's JSON value.
func readString(entry *models.StateEntry, field string) string {
	if entry == nil || entry.Value == nil {
		return ""
	}

	value, ok := (*entry.Value)[field].(string)
	if !ok {
		return ""
	}

	return value
}

// readTime parses an RFC3339 field off a state entry, falling back to
// fallback when absent or unparseable.
func readTime(entry *models.StateEntry, field string, fallback time.Time) time.Time {
	raw := readString(entry, field)
	if raw == "" {
		return fallback
	}

	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return fallback
	}

	return parsed
}
