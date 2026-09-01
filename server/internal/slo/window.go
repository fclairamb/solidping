// Package slo computes service-level-objective attainment and error budgets on
// top of the existing uptimebar availability engine.
//
// Nothing here is stored. Attainment, budget and history are always recomputed
// from rollups at read time — `month` rollups are permanent
// (internal/systemconfig/retention.go), so full history is answerable with zero
// new storage, and an SLO created today immediately knows about last year.
package slo

import (
	"errors"
	"time"
)

// ErrInvalidTimezone is returned when a timezone string is not a loadable IANA
// zone.
var ErrInvalidTimezone = errors.New("invalid timezone")

// Window is a half-open reporting period [Start, End).
type Window struct {
	Start time.Time
	End   time.Time
}

// Seconds returns the window length in seconds. Because the bounds are real
// instants rather than wall-clock arithmetic, a DST-shortened month is honestly
// an hour shorter — which is what an error budget expressed in seconds must be.
func (w Window) Seconds() float64 {
	return w.End.Sub(w.Start).Seconds()
}

// Contains reports whether t falls in [Start, End).
func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.Start) && t.Before(w.End)
}

// LoadLocation resolves an IANA timezone, defaulting to UTC when empty.
func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.UTC, nil
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrInvalidTimezone
	}

	return loc, nil
}

// MonthWindow returns the calendar month containing `at`, in `loc`.
//
// The bounds are built with time.Date in `loc` rather than by adding 24h-days,
// so a month containing a DST transition is 1h shorter or longer than 30×24h —
// exactly as the wall clock experienced it. That matters: "99.9% monthly" is a
// contract about a calendar month, not about 2_592_000 seconds.
func MonthWindow(loc *time.Location, at time.Time) Window {
	local := at.In(loc)
	start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)

	return Window{Start: start, End: start.AddDate(0, 1, 0)}
}

// PreviousMonthWindows returns the `count` calendar months ending with (and
// including) the month containing `at`, most recent first.
func PreviousMonthWindows(loc *time.Location, at time.Time, count int) []Window {
	if count <= 0 {
		return nil
	}

	current := MonthWindow(loc, at)
	windows := make([]Window, 0, count)

	for i := range count {
		start := current.Start.AddDate(0, -i, 0)
		windows = append(windows, Window{Start: start, End: start.AddDate(0, 1, 0)})
	}

	return windows
}

// WeekWindow returns the ISO week (Monday 00:00 local through the next Monday)
// containing `at`, in `loc`.
func WeekWindow(loc *time.Location, at time.Time) Window {
	local := at.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	// Go's Weekday has Sunday = 0; shift so Monday = 0.
	offset := (int(day.Weekday()) + 6) % 7
	start := day.AddDate(0, 0, -offset)

	return Window{Start: start, End: start.AddDate(0, 0, 7)}
}

// PreviousWindow returns the period immediately before the one containing
// `at`, for the given frequency ("weekly" / "monthly"). It is what the report
// job asks for: "which period just closed?".
func PreviousWindow(loc *time.Location, now time.Time, weekly bool) Window {
	if weekly {
		current := WeekWindow(loc, now)
		start := current.Start.AddDate(0, 0, -7)

		return Window{Start: start, End: start.AddDate(0, 0, 7)}
	}

	current := MonthWindow(loc, now)
	start := current.Start.AddDate(0, -1, 0)

	return Window{Start: start, End: start.AddDate(0, 1, 0)}
}

// PrecedingWindow returns the period immediately BEFORE an already-resolved
// report window — what a period-over-period comparison needs ("versus last
// month", "versus last week").
//
// It is calendar-aware on purpose, which is the whole reason it exists rather
// than the caller subtracting a duration: "the month before March" is February,
// which is 28 days, not 31, and "the month before" a month containing a DST
// transition is an hour longer or shorter than 30x24h. Subtracting
// window.End-window.Start would compare March against a 31-day slab reaching
// back into January.
//
// The bounds are anchored to the given window rather than recomputed from a
// "now": End is exactly window.Start (in loc), so the two windows are adjacent
// and half-open with no gap and no overlap, whatever calendar arithmetic the
// caller used to build the current one.
func PrecedingWindow(loc *time.Location, window Window, weekly bool) Window {
	if loc == nil {
		loc = time.UTC
	}

	end := window.Start.In(loc)

	if weekly {
		return Window{Start: end.AddDate(0, 0, -7), End: end}
	}

	return Window{Start: end.AddDate(0, -1, 0), End: end}
}
