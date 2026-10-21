package notifications

import (
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// Keys of incident.Details["regions"], written by handlers/incidents
// (regionsSnapshot). Keep the strings in sync with that side and with the
// checks API block RegionalIssueResponse.
const (
	regionsDetailsKey     = "regions"
	regionsKeyFailing     = "failingRegions"
	regionsKeyRegionCount = "regionCount"
	regionsKeyFailQuorum  = "failQuorum"

	firstResultDetailsKey = "first_result"
	lastFailureDetailsKey = "last_failure"
	snapshotRegionKey     = "region"
)

// RegionLabel is how a region is shown to a human: the dashboard's
// "{emoji} {name}".
type RegionLabel struct {
	Emoji string
	Name  string
}

// RegionInfo says which regions saw an incident's failure (spec
// 2026-10-12-15). Failing holds slugs: machines (the webhook) consume those,
// and Labels turns them into the text humans read.
type RegionInfo struct {
	// Failing is the slugs of the regions that failed, in the check's order.
	Failing []string
	// RegionCount is how many regions the check runs from (1 when only the
	// triggering result's region is known).
	RegionCount int
	// FailQuorum is how many of them must fail for the check to be down.
	FailQuorum int
	// Labels maps a slug to its display label. Filled at send time by the
	// notification job; a slug with no entry is shown as the raw slug.
	Labels map[string]RegionLabel
}

// RegionInfoFor extracts the regions from an incident, or nil when none is
// known or the incident is a burn or degraded one (they have their own field
// sets). The `regions` snapshot wins; otherwise the region of the result that
// triggered the incident (last_failure after a relapse, else first_result).
func RegionInfoFor(incident *models.Incident) *RegionInfo {
	if incident == nil || incident.Details == nil {
		return nil
	}

	if incident.Kind == models.IncidentKindSLOBurn || incident.Kind == models.IncidentKindDegraded {
		return nil
	}

	if info := regionInfoFromSnapshot(incident.Details[regionsDetailsKey]); info != nil {
		return info
	}

	for _, key := range []string{lastFailureDetailsKey, firstResultDetailsKey} {
		snapshot := detailsObject(incident.Details[key])
		if slug := jsonString(snapshot[snapshotRegionKey]); slug != "" {
			return &RegionInfo{Failing: []string{slug}, RegionCount: 1, FailQuorum: 1}
		}
	}

	return nil
}

func regionInfoFromSnapshot(raw any) *RegionInfo {
	block := detailsObject(raw)
	if block == nil {
		return nil
	}

	var failing []string

	switch list := block[regionsKeyFailing].(type) {
	case []string:
		failing = append(failing, list...)
	case []any:
		for _, item := range list {
			if slug := jsonString(item); slug != "" {
				failing = append(failing, slug)
			}
		}
	}

	if len(failing) == 0 {
		return nil
	}

	return &RegionInfo{
		Failing:     failing,
		RegionCount: int(jsonFloat(block[regionsKeyRegionCount])),
		FailQuorum:  int(jsonFloat(block[regionsKeyFailQuorum])),
	}
}

// detailsObject reads a nested JSON object, whether it still is the Go value
// the writer built (models.JSONMap) or came back from the database.
func detailsObject(raw any) map[string]any {
	switch typed := raw.(type) {
	case map[string]any:
		return typed
	case models.JSONMap:
		return typed
	default:
		return nil
	}
}

// Text renders `🇪🇺 Europe West, 🇺🇸 US East (2 of 3 regions)`. With one region
// of one it renders the label alone, with no "1 of 1".
func (r *RegionInfo) Text() string {
	return r.render(i18n.English, true)
}

// PlainText is Text without the emoji, for channels where an emoji costs
// something (an SMS turns UCS-2 with one, halving its length).
func (r *RegionInfo) PlainText() string {
	return r.render(i18n.English, false)
}

func (r *RegionInfo) render(loc i18n.Locale, withEmoji bool) string {
	if r == nil || len(r.Failing) == 0 {
		return ""
	}

	names := make([]string, 0, len(r.Failing))
	for _, slug := range r.Failing {
		names = append(names, r.label(slug, withEmoji))
	}

	text := strings.Join(names, ", ")
	if r.RegionCount > 1 {
		text += " " + loc.Tn("incident.regions.count", len(r.Failing), "total", r.RegionCount)
	}

	return text
}

func (r *RegionInfo) label(slug string, withEmoji bool) string {
	label, ok := r.Labels[slug]
	if !ok || label.Name == "" {
		return slug
	}

	if withEmoji && label.Emoji != "" {
		return label.Emoji + " " + label.Name
	}

	return label.Name
}

// regionLinePrefixIn is the wording by event: "Failing from" while the
// incident is open, "Failed from" once resolved. Empty for events that carry
// no region line (comment, ack, unack, anything else).
func regionLinePrefixIn(loc i18n.Locale, eventType string) string {
	switch eventType {
	case string(models.EventTypeIncidentCreated),
		string(models.EventTypeIncidentReopened),
		string(models.EventTypeIncidentEscalated),
		eventTypeIncidentReminder:
		return loc.T("incident.regions.failingFrom")
	case string(models.EventTypeIncidentResolved):
		return loc.T("incident.regions.failedFrom")
	default:
		return ""
	}
}

// RegionLabelFor returns the region field's title ("Failing from" or "Failed
// from") and its value for a payload, or two empty strings when this payload
// shows no region line. Every sender calls this so the rule lives once.
func RegionLabelFor(payload *Payload) (string, string) {
	return regionLabelForIn(i18n.English, payload)
}

// regionLabelForIn is RegionLabelFor in the reader's language (email).
func regionLabelForIn(loc i18n.Locale, payload *Payload) (string, string) {
	return regionLine(loc, payload, true)
}

// RegionLabelPlainFor is RegionLabelFor without emoji (Twilio).
func RegionLabelPlainFor(payload *Payload) (string, string) {
	return regionLine(i18n.English, payload, false)
}

// RegionLine is the whole line, `Failing from: <regions>`, or "" when the
// payload shows none. For senders that render plain lines.
func RegionLine(payload *Payload) string {
	title, text := regionLine(i18n.English, payload, true)
	if text == "" {
		return ""
	}

	return title + ": " + text
}

func regionLine(loc i18n.Locale, payload *Payload, withEmoji bool) (string, string) {
	if payload == nil || payload.Regions == nil {
		return "", ""
	}

	title := regionLinePrefixIn(loc, payload.EventType)
	if title == "" {
		return "", ""
	}

	if payload.Incident != nil &&
		(payload.Incident.Kind == models.IncidentKindSLOBurn || payload.Incident.Kind == models.IncidentKindDegraded) {
		return "", ""
	}

	text := payload.Regions.render(loc, withEmoji)
	if text == "" {
		return "", ""
	}

	return title, text
}

// writeRegionLine appends the region line to a plain-text body, on its own
// line. Nothing is written when the payload shows no region line.
func writeRegionLine(builder *strings.Builder, payload *Payload) {
	line := RegionLine(payload)
	if line == "" {
		return
	}

	if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "\n") {
		builder.WriteString("\n")
	}

	builder.WriteString(line)
}
