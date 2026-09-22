package notifications

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Detail keys written by the degraded evaluator
// (handlers/incidents/degraded.go). Duplicated as literals rather than imported
// so this package keeps not depending on handlers/*.
const (
	degradedKeyFailureThreshold = "degraded_failures"
	degradedKeyFailureWindow    = "degraded_failures_window"
	degradedKeyFailureSlots     = "degraded_failure_slots"
	degradedKeyFailureCount     = "degraded_failure_count"
	degradedKeyFailureFired     = "degraded_failures_fired"
	degradedKeySlowThresholdM   = "degraded_slow"
	degradedKeySlowWindow       = "degraded_slow_window"
	degradedKeySlowSlots        = "degraded_slow_slots"
	degradedKeySlowCount        = "degraded_slow_count"
	degradedKeySlowFired        = "degraded_slow_fired"
	degradedKeySlowMs           = "slow_threshold_ms"
	degradedKeyAvailabilityPct  = "degraded_availability_pct"
	degradedKeyWindowFrom       = "degraded_window_from"
	degradedKeyWindowTo         = "degraded_window_to"
	degradedKeyCurrentlyUp      = "degraded_currently_up"
)

// DegradedInfo is the degraded-specific half of a notification.
//
// Its whole job is to make a degraded message NOT read like an outage. An
// operator who gets "acme.com is down" for an intermittence pattern mutes the
// channel, and then the real outage is muted too — so the wording leads with the
// counts, names the window, and says plainly that the target is up right now.
type DegradedInfo struct {
	FailuresFired    bool
	FailureCount     int
	FailureSlots     int
	FailureThreshold int
	FailureWindow    int

	SlowFired     bool
	SlowCount     int
	SlowSlots     int
	SlowThreshold int
	SlowWindow    int
	SlowMs        int

	// AvailabilityPct is the availability over the failure window. Nil when the
	// failure rule was not evaluated.
	AvailabilityPct *float64

	// WindowFrom / WindowTo bound the probes the numbers describe.
	WindowFrom time.Time
	WindowTo   time.Time

	// CurrentlyUp is the check's live status at decision time.
	CurrentlyUp bool
}

// DegradedInfoFor extracts the degraded payload from an incident, or nil when
// the incident is not a degraded one.
func DegradedInfoFor(incident *models.Incident) *DegradedInfo {
	if incident == nil || incident.Kind != models.IncidentKindDegraded || incident.Details == nil {
		return nil
	}

	details := incident.Details

	info := &DegradedInfo{
		FailuresFired:    jsonBool(details[degradedKeyFailureFired]),
		FailureCount:     int(jsonFloat(details[degradedKeyFailureCount])),
		FailureSlots:     int(jsonFloat(details[degradedKeyFailureSlots])),
		FailureThreshold: int(jsonFloat(details[degradedKeyFailureThreshold])),
		FailureWindow:    int(jsonFloat(details[degradedKeyFailureWindow])),
		SlowFired:        jsonBool(details[degradedKeySlowFired]),
		SlowCount:        int(jsonFloat(details[degradedKeySlowCount])),
		SlowSlots:        int(jsonFloat(details[degradedKeySlowSlots])),
		SlowThreshold:    int(jsonFloat(details[degradedKeySlowThresholdM])),
		SlowWindow:       int(jsonFloat(details[degradedKeySlowWindow])),
		SlowMs:           int(jsonFloat(details[degradedKeySlowMs])),
		WindowFrom:       jsonTime(details[degradedKeyWindowFrom]),
		WindowTo:         jsonTime(details[degradedKeyWindowTo]),
		CurrentlyUp:      jsonBool(details[degradedKeyCurrentlyUp]),
	}

	if raw, present := details[degradedKeyAvailabilityPct]; present {
		pct := jsonFloat(raw)
		info.AvailabilityPct = &pct
	}

	return info
}

// Headline is the one line every channel leads with. Deliberately not the word
// "incident" and never "down".
func (d *DegradedInfo) Headline(checkName string) string {
	return fmt.Sprintf("%s is degraded: %s", checkName, d.Reason())
}

// Reason states which rule fired, in probe counts rather than adjectives.
func (d *DegradedInfo) Reason() string {
	switch {
	case d.FailuresFired && d.SlowFired:
		return fmt.Sprintf("%s, and %s", d.failureReason(), d.slowReason())
	case d.FailuresFired:
		return d.failureReason()
	case d.SlowFired:
		return d.slowReason()
	default:
		return "an intermittence pattern was detected"
	}
}

func (d *DegradedInfo) failureReason() string {
	if d.AvailabilityPct != nil {
		return fmt.Sprintf("%d failures in the last %d probes (%.1f%%)",
			d.FailureCount, d.FailureSlots, *d.AvailabilityPct)
	}

	return fmt.Sprintf("%d failures in the last %d probes", d.FailureCount, d.FailureSlots)
}

func (d *DegradedInfo) slowReason() string {
	return fmt.Sprintf("%d of the last %d probes were slower than %dms",
		d.SlowCount, d.SlowSlots, d.SlowMs)
}

// StatusText is the sentence that stops the message reading like an outage.
func (d *DegradedInfo) StatusText() string {
	if d.CurrentlyUp {
		return "Currently up."
	}

	return "Currently not up."
}

// SummaryLines is the shared body every non-templated channel appends, so a
// Slack message, a Discord embed and an email all say the same things in the
// same order.
func (d *DegradedInfo) SummaryLines() []string {
	out := make([]string, 0, 3)

	if d.FailuresFired {
		out = append(out, fmt.Sprintf("Failures: %s (rule: %d of %d probes)",
			d.failureReason(), d.FailureThreshold, d.FailureWindow))
	}

	if d.SlowFired {
		out = append(out, fmt.Sprintf("Slow: %s (rule: %d of %d probes over %dms)",
			d.slowReason(), d.SlowThreshold, d.SlowWindow, d.SlowMs))
	}

	return append(out, d.StatusText())
}

// WindowText names the span the numbers describe.
func (d *DegradedInfo) WindowText() string {
	if d.WindowFrom.IsZero() || d.WindowTo.IsZero() {
		return "recent probes"
	}

	return d.WindowFrom.UTC().Format("15:04") + " – " + d.WindowTo.UTC().Format("15:04 MST")
}

// DegradedCheckWindowURL is the deep link a degraded notification carries: the
// check's page, zoomed to the window the numbers came from.
//
// Isolated red dots spread over an hour do not read as an event; the point of
// linking INTO the window (`graphFrom` / `graphTo`, the params the chart's
// drag-to-zoom already writes) is that the reader lands on the span rather than
// on a default 24 h view where seven failures are seven pixels.
func DegradedCheckWindowURL(
	baseURL, orgSlug string, check *models.Check, info *DegradedInfo,
) string {
	base := checkDashURL(baseURL, orgSlug, check)
	if base == "" || info == nil || info.WindowFrom.IsZero() || info.WindowTo.IsZero() {
		return base
	}

	query := url.Values{}
	query.Set("graphFrom", strconv.FormatInt(info.WindowFrom.UnixMilli(), 10))
	query.Set("graphTo", strconv.FormatInt(info.WindowTo.UnixMilli(), 10))

	return base + "?" + query.Encode()
}

// jsonBool reads a JSONB boolean defensively: the same map arrives either
// straight from the evaluator (native bool) or round-tripped through the
// database.
func jsonBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "true"
	default:
		return false
	}
}
