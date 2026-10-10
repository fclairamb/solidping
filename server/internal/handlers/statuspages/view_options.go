package statuspages

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// includeParam is the query parameter narrowing the optional payload
// sections a public status-page view returns (spec 2026-09-22-07).
const includeParam = "include"

// Include tokens. Case-sensitive: they mirror the JSON field names on the
// wire, not the query-parameter convention used elsewhere in the API.
const (
	includeTokenAvailability = "availability"
	includeTokenResponseTime = "responseTime"
	includeTokenUpdates      = "updates"
)

// updatesDaysParam widens the `updates` window beyond the default cap. It is
// bounded by the page's own historyDays at compute time.
const updatesDaysParam = "updatesDays"

// DefaultUpdatesDays is the window of the `updates` section when the caller
// names `updates` in an explicit `include` and does not pass `updatesDays`.
const DefaultUpdatesDays = 7

// ViewOptions narrows which optional, expensive sections a public
// status-page view computes and returns. It only ever NARROWS what the
// page's own settings would produce — ANDing with ShowAvailability /
// ShowResponseTime, never widening them.
type ViewOptions struct {
	Availability bool
	ResponseTime bool
	// Updates includes the recentUpdates timeline.
	Updates bool
	// UpdatesDays is the timeline window in days. 0 means the page's full
	// historyDays (the legacy, no-`include` behaviour); otherwise it is capped
	// at historyDays when computed.
	UpdatesDays int
}

// AllViewOptions is the default shape: every optional section included. This
// is what a request with no `include` parameter gets, and it is what every
// pre-existing caller of ViewStatusPage / ViewDefaultStatusPage effectively
// asked for before the parameter existed — the compatibility guarantee.
func AllViewOptions() ViewOptions {
	return ViewOptions{Availability: true, ResponseTime: true, Updates: true}
}

// InvalidIncludeError is returned by ParseViewOptions when the `include`
// query parameter names a token outside the valid set. The handler maps it
// to 400 VALIDATION_ERROR.
type InvalidIncludeError struct {
	Token string
}

func (e *InvalidIncludeError) Error() string {
	return fmt.Sprintf(
		"invalid include value %q: valid values are %s, %s, %s",
		e.Token, includeTokenAvailability, includeTokenResponseTime, includeTokenUpdates,
	)
}

// ParseViewOptions parses the `include` query parameter into ViewOptions.
//
//   - Absent entirely -> AllViewOptions() (today's payload, byte-identical).
//   - Present but empty (`include=`) -> neither section.
//   - Comma-separated, unordered, duplicates and surrounding whitespace
//     ignored (`include=availability,` is just `availability`).
//   - `updates` adds the recentUpdates timeline, capped at DefaultUpdatesDays
//     unless `updatesDays=N` (a positive integer, bounded by the page's
//     historyDays when computed) asks for more.
//   - Any token outside {availability, responseTime, updates} is a 400, naming
//     the offending token.
func ParseViewOptions(values url.Values) (ViewOptions, error) {
	if !values.Has(includeParam) {
		return AllViewOptions(), nil
	}

	var opts ViewOptions

	for _, raw := range strings.Split(values.Get(includeParam), ",") {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}

		switch token {
		case includeTokenAvailability:
			opts.Availability = true
		case includeTokenResponseTime:
			opts.ResponseTime = true
		case includeTokenUpdates:
			opts.Updates = true
			opts.UpdatesDays = DefaultUpdatesDays
		default:
			return ViewOptions{}, &InvalidIncludeError{Token: token}
		}
	}

	if opts.Updates && values.Has(updatesDaysParam) {
		days, err := strconv.Atoi(strings.TrimSpace(values.Get(updatesDaysParam)))
		if err != nil || days < 1 {
			return ViewOptions{}, &InvalidUpdatesDaysError{Value: values.Get(updatesDaysParam)}
		}

		opts.UpdatesDays = days
	}

	return opts, nil
}

// InvalidUpdatesDaysError is returned when `updatesDays` is not a positive
// integer. The handler maps it to 400 VALIDATION_ERROR.
type InvalidUpdatesDaysError struct {
	Value string
}

func (e *InvalidUpdatesDaysError) Error() string {
	return fmt.Sprintf("invalid updatesDays value %q: must be a positive integer", e.Value)
}
