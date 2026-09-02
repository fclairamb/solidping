package uptimereport

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// maxStripDays bounds the strip so a misconfigured schedule can never render a
// runaway row. A monthly window needs at most 32 cells (see dayStripPlan).
const maxStripDays = 40

// dayStripPlan is the resolved geometry of the per-day strip.
type dayStripPlan struct {
	start time.Time // UTC-day aligned, at or before the window start
	days  int
}

// planDayStrip resolves the strip's cells for a report window.
//
// The cells are UTC DAYS, not local days, and that is a deliberate correctness
// choice rather than a shortcut:
//
//   - uptimebar.BucketAvailability keys every row on
//     periodStart.Truncate(bucketDuration), and Truncate is relative to the
//     zero instant — so a 24 h bucket series only lines up with the rows that
//     belong in it when it STARTS on a UTC-day boundary too (the same rule
//     availability/buckets.go's planBuckets documents).
//   - the day-tier rollups themselves are UTC-day aligned by construction (the
//     aggregation job buckets on UTC), and a monthly report on the default
//     retention is mostly day rollups. Folding those into local days would
//     attribute a whole day of data to the wrong cell for every zone with a
//     negative UTC offset, and would leave the last local day of the month
//     permanently gray.
//
// So the strip is honest about what it is and the template labels it "(UTC)"
// rather than claiming a local-day precision the stored data cannot support.
// The cell count is therefore 7-8 for a weekly window and 28-32 for a monthly
// one: every UTC day the window touches.
func planDayStrip(window slo.Window) dayStripPlan {
	start := window.Start.UTC().Truncate(24 * time.Hour)

	span := window.End.UTC().Sub(start)
	if span <= 0 {
		return dayStripPlan{start: start, days: 0}
	}

	days := int(span / (24 * time.Hour))
	if span%(24*time.Hour) != 0 {
		days++
	}

	if days > maxStripDays {
		days = maxStripDays
	}

	return dayStripPlan{start: start, days: days}
}

// label names the strip's span and its bucketing timezone.
func (p dayStripPlan) label() string {
	if p.days == 0 {
		return ""
	}

	last := p.start.AddDate(0, 0, p.days-1)

	// Plain text, no HTML entities: this string lands in the view model, and
	// html/template escapes a "&middot;" into a visible "&amp;middot;". The
	// plain-text part of the mail reads it too.
	return fmt.Sprintf("Daily availability, %s – %s (UTC)",
		p.start.Format("2 Jan"), last.Format("2 Jan"))
}

// dayStrips returns the per-check day strip, run-length encoded.
//
// A failure here is NOT fatal to the report: the strip is decoration on top of
// numbers the reader came for, so a bucketing error is logged and the digest
// ships without strips rather than not shipping at all.
func (b *Builder) dayStrips(
	ctx context.Context, orgUID string, checkUIDs []string, plan dayStripPlan, hints uptimebar.Hints,
) map[string][]DayCell {
	if plan.days == 0 || len(checkUIDs) == 0 {
		return nil
	}

	byCheck, err := uptimebar.BucketAvailability(
		ctx, b.db, orgUID, checkUIDs, 24*time.Hour, plan.start, plan.days, hints)
	if err != nil {
		slog.WarnContext(ctx, "uptime report could not build the per-day strip; "+
			"sending the digest without it",
			"organization_uid", orgUID, "error", err)

		return nil
	}

	out := make(map[string][]DayCell, len(checkUIDs))

	for _, checkUID := range checkUIDs {
		buckets := byCheck[checkUID]

		colors := make([]string, 0, plan.days)
		for day := range plan.days {
			colors = append(colors, dayStripColor(buckets[plan.start.AddDate(0, 0, day)]))
		}

		out[checkUID] = encodeDayCells(colors)
	}

	return out
}

// encodeDayCells collapses runs of identical colors into one cell each.
func encodeDayCells(colors []string) []DayCell {
	if len(colors) == 0 {
		return nil
	}

	cells := make([]DayCell, 0, len(colors))

	for _, color := range colors {
		last := len(cells) - 1
		if last >= 0 && cells[last].Color == color {
			cells[last].Span++
			cells[last].Wide = true

			continue
		}

		cells = append(cells, DayCell{Color: color, Span: 1})
	}

	return cells
}

// maxStripCells is the total number of day cells the whole report may render,
// across every row.
//
// It is a HARD byte bound, not a nicety. The strip is per-row x up to
// maxCheckRows rows, and a mail Gmail clips at ~102 KB is a mail whose footer,
// unsubscribe link and objectives simply disappear behind a "[Message clipped]"
// link. Run-length encoding already collapses the realistic cases to a handful
// of cells per row; this bounds the pathological one (a month in which every
// check changes state every single day), which would otherwise be 50 x 31
// cells.
//
// The budget is spent in the table's own order, which is worst-uptime first —
// so when it does run out, the rows that lose their strip are the healthy ones
// at the bottom, which is exactly the right bias.
const maxStripCells = 900

// applyStripBudget drops strips past maxStripCells, in table order, and returns
// how many rows kept theirs plus how many wanted one. It never renders a
// PARTIAL strip: a half-drawn month would be read as "no data after the 14th"
// rather than as "we ran out of room".
//
// Once the budget bites, the row still shows its percentage but no strip — and
// a row with a number and a blank space where its neighbors have a colored bar
// is indistinguishable from a rendering failure. The counts returned here are
// what lets the template SAY it happened, exactly as the row cap says "showing
// the 50 lowest-uptime checks of N" instead of silently capping.
func applyStripBudget(rows []CheckRow) (int, int) {
	spent, shown, wanted := 0, 0, 0

	for i := range rows {
		cells := len(rows[i].Days)
		if cells == 0 {
			continue
		}

		wanted++

		if spent+cells > maxStripCells {
			rows[i].Days = nil

			continue
		}

		spent += cells
		shown++
	}

	return shown, wanted
}

// stripBudgetNote is the one-line statement rendered when the budget dropped at
// least one strip. Deliberately factual and colorless: it explains a layout
// limit, not a state of the monitored service.
func stripBudgetNote(shown int) string {
	if shown == 1 {
		return "Daily strips are shown for the lowest-uptime check only; " +
			"the rest were left out to keep this email under the size mail clients truncate at."
	}

	return fmt.Sprintf(
		"Daily strips are shown for the %d lowest-uptime checks; "+
			"the rest were left out to keep this email under the size mail clients truncate at.",
		shown)
}
