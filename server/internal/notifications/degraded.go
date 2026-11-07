package notifications

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
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
	degradedKeyTurnedOff        = "degraded_turned_off"
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

	// TurnedOff is set on an incident closed because degraded detection was
	// turned off on its check (spec 2026-09-24-08). Its resolved notification
	// must not say the check is steady again: nobody knows, the evaluator
	// simply stopped looking.
	TurnedOff bool
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
		TurnedOff:        jsonBool(details[degradedKeyTurnedOff]),
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
	return d.reasonIn(i18n.English)
}

// reasonIn is Reason in the reader's language.
func (d *DegradedInfo) reasonIn(loc i18n.Locale) string {
	switch {
	case d.FailuresFired && d.SlowFired:
		return loc.T("incident.degraded.both", "failures", d.failureReasonIn(loc), "slow", d.slowReasonIn(loc))
	case d.FailuresFired:
		return d.failureReasonIn(loc)
	case d.SlowFired:
		return d.slowReasonIn(loc)
	default:
		return loc.T("incident.degraded.pattern")
	}
}

func (d *DegradedInfo) failureReasonIn(loc i18n.Locale) string {
	if d.AvailabilityPct != nil {
		return loc.Tn("incident.degraded.failures", d.FailureCount,
			"slots", d.FailureSlots, "availability", oneDecimalIn(loc, *d.AvailabilityPct))
	}

	return loc.Tn("incident.degraded.failuresNoAvailability", d.FailureCount, "slots", d.FailureSlots)
}

func (d *DegradedInfo) slowReasonIn(loc i18n.Locale) string {
	return loc.Tn("incident.degraded.slow", d.SlowCount, "slots", d.SlowSlots, "ms", d.SlowMs)
}

// failureLineIn is the failure rule's line of the degraded mail: the reason
// and the rule it broke.
func (d *DegradedInfo) failureLineIn(loc i18n.Locale) string {
	return loc.T("incident.degraded.failureLine", "reason", d.failureReasonIn(loc),
		"threshold", d.FailureThreshold, "window", d.FailureWindow)
}

// slowLineIn is the slow rule's line of the degraded mail.
func (d *DegradedInfo) slowLineIn(loc i18n.Locale) string {
	return loc.T("incident.degraded.slowLine", "reason", d.slowReasonIn(loc),
		"threshold", d.SlowThreshold, "window", d.SlowWindow, "ms", d.SlowMs)
}

// StatusText is the sentence that stops the message reading like an outage.
func (d *DegradedInfo) StatusText() string {
	return d.statusTextIn(i18n.English)
}

// statusTextIn is StatusText in the reader's language.
func (d *DegradedInfo) statusTextIn(loc i18n.Locale) string {
	if d.CurrentlyUp {
		return loc.T("incident.degraded.currentlyUp")
	}

	return loc.T("incident.degraded.currentlyNotUp")
}

// SummaryLines is the shared body every non-templated channel appends, so a
// Slack message, a Discord embed and an email all say the same things in the
// same order.
func (d *DegradedInfo) SummaryLines() []string {
	out := make([]string, 0, 3)

	if d.FailuresFired {
		out = append(out, fmt.Sprintf("Failures: %s (rule: %d of %d probes)",
			d.failureReasonIn(i18n.English), d.FailureThreshold, d.FailureWindow))
	}

	if d.SlowFired {
		out = append(out, fmt.Sprintf("Slow: %s (rule: %d of %d probes over %dms)",
			d.slowReasonIn(i18n.English), d.SlowThreshold, d.SlowWindow, d.SlowMs))
	}

	return append(out, d.StatusText())
}

// WindowText names the span the numbers describe.
func (d *DegradedInfo) WindowText() string {
	return d.windowTextIn(i18n.English)
}

// windowTextIn is WindowText in the reader's language and timezone:
// "14:02 – 14:47 UTC", "16:02 – 16:47 CEST".
func (d *DegradedInfo) windowTextIn(loc i18n.Locale) string {
	if d.WindowFrom.IsZero() || d.WindowTo.IsZero() {
		return loc.T("incident.degraded.recentProbes")
	}

	from := d.WindowFrom.In(loc.Location())
	to := d.WindowTo.In(loc.Location())

	return from.Format("15:04") + " – " + to.Format("15:04") + " " + zoneLabel(to)
}

// zoneLabel is a time's zone abbreviation, "UTC+03" for the zones the tz
// database names by offset only (the same rule as the date template func).
func zoneLabel(when time.Time) string {
	abbr := when.Format("MST")
	if strings.HasPrefix(abbr, "+") || strings.HasPrefix(abbr, "-") {
		return "UTC" + abbr
	}

	return abbr
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

// Plain-text framing of a degraded incident, for the senders that build their
// own titles and bodies rather than templates (ntfy, Gotify, Matrix, Teams...).
// A degraded incident rides the same incident.created / incident.resolved
// events as an outage, so the event type alone would title it "[DOWN]" and give
// "Check failed" as its cause: these helpers let the incident's kind decide.
const (
	downTag              = "[DOWN] "
	degradedTag          = "[DEGRADED] "
	degradedClearedTag   = "[STEADY AGAIN] "
	degradedTurnedOffTag = "[DEGRADED DETECTION OFF] "
	degradedWindowLabel  = "Window"
)

// isDegradedIncident reports whether the incident is a degraded one. It reads
// the kind alone, so an incident whose details are missing still reads as
// degraded (DegradedInfoFor needs the details).
func isDegradedIncident(incident *models.Incident) bool {
	return incident != nil && incident.Kind == models.IncidentKindDegraded
}

// opensIncident reports whether the event announces an open problem (created,
// escalated, reopened): the events whose priority and color an outage raises
// and a degradation must not.
func opensIncident(eventType string) bool {
	return eventType == eventTypeIncidentCreated ||
		eventType == eventTypeIncidentEscalated ||
		eventType == eventTypeIncidentReopened
}

// openedTag is the title prefix of an incident.created event: "[DOWN] " for an
// outage, "[DEGRADED] " for a degraded incident.
func openedTag(incident *models.Incident) string {
	if isDegradedIncident(incident) {
		return degradedTag
	}

	return downTag
}

// degradedMark goes after the tag of the events that already carry one
// ("[ESCALATED] ", "[REOPENED] "), so they still say the incident is a
// degradation. Empty for every other kind.
func degradedMark(incident *models.Incident) string {
	if isDegradedIncident(incident) {
		return degradedTag
	}

	return ""
}

// degradedCause is the cause line of a degraded incident: the rule that fired,
// in probe counts. Without details it falls back to the incident's own title
// ("acme is degraded: 5 failures in the last 60 probes"), which the evaluator
// writes from the same numbers.
func degradedCause(incident *models.Incident) string {
	if info := DegradedInfoFor(incident); info != nil {
		return info.Reason()
	}

	if incident != nil && incident.Title != nil && *incident.Title != "" {
		return *incident.Title
	}

	return i18n.English.T("incident.degraded.pattern")
}

// degradedHeadline is the one-line title of an open degraded incident:
// "acme is degraded: <reason>".
func degradedHeadline(incident *models.Incident, checkName string) string {
	if info := DegradedInfoFor(incident); info != nil {
		return info.Headline(checkName)
	}

	return checkName + " is degraded"
}

// degradedFact is one labeled line of a degraded notification.
type degradedFact struct {
	Label string
	Value string
}

// degradedFacts is the lines an open degraded incident shows in place of the
// outage's "Cause / Failure count": what fired, whether the target is up right
// now, and the window the numbers describe. Without details only the cause is
// known.
func degradedFacts(incident *models.Incident) []degradedFact {
	facts := []degradedFact{{Label: mmFieldCause, Value: degradedCause(incident)}}

	if info := DegradedInfoFor(incident); info != nil {
		facts = append(facts,
			degradedFact{Label: fieldLabelStatus, Value: info.StatusText()},
			degradedFact{Label: degradedWindowLabel, Value: info.WindowText()},
		)
	}

	return facts
}

// degradedPlainLines is degradedFacts as "Label: value" lines.
func degradedPlainLines(incident *models.Incident) []string {
	facts := degradedFacts(incident)
	lines := make([]string, 0, len(facts))

	for i := range facts {
		lines = append(lines, facts[i].Label+": "+facts[i].Value)
	}

	return lines
}

// writeDegradedLines writes degradedPlainLines to a plain-text body, each on
// its own line.
func writeDegradedLines(builder *strings.Builder, incident *models.Incident) {
	for _, line := range degradedPlainLines(incident) {
		builder.WriteString(line + "\n")
	}
}

// degradedTurnedOff reports whether a degraded incident was closed because
// degraded detection was turned off on its check.
func degradedTurnedOff(incident *models.Incident) bool {
	info := DegradedInfoFor(incident)

	return info != nil && info.TurnedOff
}

// degradedResolvedTitle is the prose title of a resolved degraded incident: it
// was never down, so it did not "resolve" or "recover", it is steady again (or
// nobody knows, when detection was turned off).
func degradedResolvedTitle(incident *models.Incident, checkName string) string {
	if degradedTurnedOff(incident) {
		return "Degraded detection turned off for " + checkName
	}

	return checkName + " is steady again"
}

// degradedResolvedSentence is the lead line of a resolved degraded incident:
// the pattern cleared, or detection was turned off and the check did not
// necessarily recover. Empty for every other kind.
func degradedResolvedSentence(incident *models.Incident) string {
	if !isDegradedIncident(incident) {
		return ""
	}

	if degradedTurnedOff(incident) {
		return i18n.English.T("incident.degradedResolved.turnedOffText")
	}

	return i18n.English.T("incident.degradedResolved.textBody")
}

// resolvedLeadSentence is the line a resolved notification opens with when the
// incident did not simply recover from an outage: the check was deleted or
// disabled, or the incident was a degradation. Empty for an ordinary recovery.
func resolvedLeadSentence(incident *models.Incident) string {
	if closedWithoutRecovery(incident) {
		return closedSentence(incident)
	}

	return degradedResolvedSentence(incident)
}
