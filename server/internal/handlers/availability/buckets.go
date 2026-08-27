package availability

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// ErrInvalidWindow is returned when the from/to/bucket triple does not describe
// a usable window (missing or unparseable instants, to <= from, a bucket that is
// not a whole positive multiple of an hour, or a cell count past the safety cap).
var ErrInvalidWindow = errors.New("invalid availability window")

const (
	// minBucket is the hard floor on bucket width. Below an hour a percentage is
	// noise: a 5-minute slice of a 1-minute check quantizes to 0/20/40/…%, so the
	// cell colour would swing on a single probe. At an hour a 1-minute check has
	// ~60 samples and the number means something. Every accepted bucket is a
	// whole multiple of this.
	minBucket = time.Hour

	// autoMaxCells is the cell count the SERVER targets when the caller does not
	// name a bucket width — the spec's drag-zoom rule ("the smallest hour-multiple
	// keeping cells <= 60"). A client that knows its own layout (the chart's
	// day/week/month presets) sends an explicit bucket instead.
	autoMaxCells = 60

	// maxBucketCells is the safety cap on how many cells ONE request may ask the
	// engine to key. It is deliberately well above autoMaxCells: the strip renders
	// at most ~60, but an API consumer may legitimately want a finer read, and the
	// cost that matters is the row scan, which is bounded by the window rather
	// than by the cell count. Past this the request is rejected rather than
	// silently truncated.
	maxBucketCells = 200
)

// AvailabilityBucket is one cell of the strip: a half-open [periodStart,
// periodEnd) slice of the window with its probe-ratio availability.
//
// AvailabilityPct is null (and HasData false) when the slice has no countable
// probes. That is the whole point of carrying both fields: "no data" is a
// distinct third state and must render gray, never as a manufactured 100%. It
// mirrors uptimebar.BucketStats.AvailabilityPct() (float64, bool).
type AvailabilityBucket struct {
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
	HasData     bool      `json:"hasData"`
	// AvailabilityPct is successfulChecks/totalChecks*100, or null when
	// totalChecks == 0.
	AvailabilityPct  *float64 `json:"availabilityPct"`
	TotalChecks      int      `json:"totalChecks"`
	SuccessfulChecks int      `json:"successfulChecks"`
	// Status is the shared up/degraded/down/noData vocabulary
	// (uptimebar.Classify) — the SAME classifier the public status page and the
	// badge uptime bar use, so identical numbers can never be painted different
	// colours on two surfaces.
	Status string `json:"status"`
}

// ListAvailabilityBucketsResponse is the bucketed-availability payload. `data`
// is the cell series (oldest → newest, aligned to bucketSeconds boundaries);
// `window` is the EXACT [from, to) fold, which is what a caller renders when the
// window is too short for a legible strip.
type ListAvailabilityBucketsResponse struct {
	Data []AvailabilityBucket `json:"data"`
	// Window is the whole-window availability over exactly the requested
	// [from, to) — not the sum of the cells. The cells are aligned OUTWARD to
	// bucket boundaries, so on a short window their sum would cover more time
	// than was asked for; the header figure must answer the question the user
	// actually asked.
	Window AvailabilityBucket `json:"window"`
	// BucketSeconds is the resolved bucket width — echoed because the server may
	// have chosen it (when the request omitted `bucket`).
	BucketSeconds int `json:"bucketSeconds"`
	// WindowStart/WindowEnd echo the resolved request window (NOT the aligned
	// cell span) so a client can position the strip against its own x-scale.
	WindowStart time.Time `json:"windowStart"`
	WindowEnd   time.Time `json:"windowEnd"`
	// Region echoes the region filter, empty when the read summed every region.
	Region string `json:"region,omitempty"`
}

// GetAvailabilityBucketsOptions carries the parsed request parameters.
type GetAvailabilityBucketsOptions struct {
	From time.Time
	To   time.Time
	// Bucket is the requested cell width; zero means "choose one" (see
	// resolveBucket).
	Bucket time.Duration
	// Region scopes the read to a single probe region. Empty sums every region
	// — summing up/total, never averaging percentages.
	Region string
}

// alignedCount is how many cells of `width` it takes to cover [from, to) once
// the series is aligned outward to a multiple of `width`. Alignment can add one
// cell relative to a naive ceil(span/width), which is exactly why the auto rule
// below counts AFTER aligning instead of before.
func alignedCount(from, to time.Time, width time.Duration) (time.Time, int) {
	start := from.UTC().Truncate(width)

	count := int(math.Ceil(float64(to.UTC().Sub(start)) / float64(width)))
	if count < 1 {
		count = 1
	}

	return start, count
}

// autoBucket picks a width when the caller left it to the server: the SMALLEST
// whole-hour multiple whose aligned cell count is at or below autoMaxCells.
//
// It starts from the naive estimate and widens by an hour at a time, because the
// outward alignment can push the count one past the estimate — checking the
// estimate alone would let a drag-zoom return 61 cells from a rule that promises
// at most 60. Expressed in whole hours throughout, so the 1-hour floor holds by
// construction.
func autoBucket(from, to time.Time) time.Duration {
	hours := int(math.Ceil(to.Sub(from).Hours() / float64(autoMaxCells)))
	if hours < 1 {
		hours = 1
	}

	for {
		width := time.Duration(hours) * time.Hour
		if _, count := alignedCount(from, to, width); count <= autoMaxCells {
			return width
		}

		hours++
	}
}

// resolveBucket validates the requested bucket width against the window, or
// picks one when the caller left it to the server.
func resolveBucket(from, to time.Time, requested time.Duration) (time.Duration, error) {
	if requested == 0 {
		return autoBucket(from, to), nil
	}

	if requested < minBucket || requested%minBucket != 0 {
		return 0, fmt.Errorf(
			"%w: bucket must be a whole positive multiple of %s, got %s",
			ErrInvalidWindow, minBucket, requested)
	}

	return requested, nil
}

// bucketPlan is the resolved cell geometry for a window.
type bucketPlan struct {
	width time.Duration
	start time.Time // aligned cell start, at or before the requested from
	count int
}

// planBuckets resolves the cell width and aligns the series.
//
// Alignment is not cosmetic: uptimebar.BucketAvailability keys each row on
// periodStart.Truncate(bucketDuration), and Truncate is relative to the zero
// instant, so cells only line up with the rows that fall in them when the series
// starts on a multiple of the width too. An unaligned start would shift every
// row into the neighbouring cell.
func planBuckets(from, to time.Time, requested time.Duration) (bucketPlan, error) {
	if from.IsZero() || to.IsZero() {
		return bucketPlan{}, fmt.Errorf("%w: from and to are both required", ErrInvalidWindow)
	}

	span := to.Sub(from)
	if span <= 0 {
		return bucketPlan{}, fmt.Errorf("%w: to must be after from", ErrInvalidWindow)
	}

	if span > time.Duration(maxLookbackYears)*365*24*time.Hour {
		return bucketPlan{}, fmt.Errorf(
			"%w: window exceeds the %d-year sanity bound", ErrInvalidWindow, maxLookbackYears)
	}

	width, err := resolveBucket(from, to, requested)
	if err != nil {
		return bucketPlan{}, err
	}

	start, count := alignedCount(from, to, width)

	if count > maxBucketCells {
		return bucketPlan{}, fmt.Errorf(
			"%w: %d buckets requested, at most %d are allowed — widen the bucket",
			ErrInvalidWindow, count, maxBucketCells)
	}

	return bucketPlan{width: width, start: start, count: count}, nil
}

// bucketDTO converts one folded BucketStats into the wire cell.
func bucketDTO(stats uptimebar.BucketStats, start, end time.Time) AvailabilityBucket {
	up, degraded := uptimebar.DefaultThresholds()

	cell := AvailabilityBucket{
		PeriodStart:      start,
		PeriodEnd:        end,
		TotalChecks:      stats.Total,
		SuccessfulChecks: stats.Up,
		Status:           uptimebar.ClassifyStats(stats, up, degraded),
	}

	if pct, ok := stats.AvailabilityPct(); ok {
		cell.HasData = true
		cell.AvailabilityPct = &pct
	}

	return cell
}

// GetAvailabilityBuckets cuts an arbitrary window into uniform, hour-aligned
// availability cells for a single check, plus the exact whole-window fold.
//
// It is a thin wrapper over the shared engine on purpose: deriving the cells
// client-side from the chart's already-fetched rows would duplicate the counting
// rules (models.Result.ExcludedFromAvailability — lifecycle markers and
// abandoned attempts out of BOTH numerator and denominator, warning counts as
// up) and drift from the availability table sitting right below the chart.
//
// Maintenance probes are counted like any other, matching the availability
// table, status pages and badges. Maintenance exclusion is deliberately SLO-only
// today (uptimebar.BucketStats.ExcludingMaintenance is never called here); a
// strip that quietly disagreed with the table next to it would be worse than one
// that counts maintenance.
func (s *Service) GetAvailabilityBuckets(
	ctx context.Context, orgSlug, checkIdent string, opts *GetAvailabilityBucketsOptions,
) (*ListAvailabilityBucketsResponse, error) {
	plan, err := planBuckets(opts.From, opts.To, opts.Bucket)
	if err != nil {
		return nil, err
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, checkIdent)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	hints := s.uptimebarHints(ctx, org.UID)

	var regions []string
	if opts.Region != "" {
		regions = []string{opts.Region}
	}

	from := opts.From.UTC()
	to := opts.To.UTC()

	var (
		byBucket map[time.Time]uptimebar.BucketStats
		windowed uptimebar.BucketStats
	)

	// The two reads are independent and each costs a pair of tier-aligned
	// queries, so fan them out rather than paying their sum. They cannot be one
	// read: the cells are aligned outward to bucket boundaries while the window
	// fold must answer exactly [from, to), and the fold additionally includes the
	// month tier, which is deliberately excluded from per-bucket attribution.
	errg, errgCtx := errgroup.WithContext(ctx)

	errg.Go(func() error {
		stats, bucketErr := uptimebar.BucketAvailabilityInRegions(
			errgCtx, s.db, org.UID, []string{check.UID}, regions,
			plan.width, plan.start, plan.count, hints)
		if bucketErr != nil {
			return bucketErr
		}

		byBucket = stats[check.UID]

		return nil
	})

	errg.Go(func() error {
		stats, windowErr := uptimebar.WindowAvailabilityInRegions(
			errgCtx, s.db, org.UID, []string{check.UID}, regions, from, to, hints)
		if windowErr != nil {
			return windowErr
		}

		windowed = stats[check.UID]

		return nil
	})

	if err := errg.Wait(); err != nil {
		return nil, err
	}

	cells := make([]AvailabilityBucket, 0, plan.count)

	for i := range plan.count {
		cellStart := plan.start.Add(time.Duration(i) * plan.width)
		// A bucket absent from the map had no rows at all — bucketDTO turns the
		// zero BucketStats into hasData=false / status noData, which is the
		// truthful answer and NOT 100%.
		cells = append(cells, bucketDTO(byBucket[cellStart], cellStart, cellStart.Add(plan.width)))
	}

	return &ListAvailabilityBucketsResponse{
		Data:          cells,
		Window:        bucketDTO(windowed, from, to),
		BucketSeconds: int(plan.width.Seconds()),
		WindowStart:   from,
		WindowEnd:     to,
		Region:        opts.Region,
	}, nil
}

// parseInstant parses a required RFC3339 query parameter.
func parseInstant(name, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("%w: %s is required (RFC3339)", ErrInvalidWindow, name)
	}

	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s must be RFC3339: %w", ErrInvalidWindow, name, err)
	}

	return parsed.UTC(), nil
}

// GetAvailabilityBuckets handles
// GET /api/v1/orgs/:org/checks/:check/availability/buckets.
func (h *Handler) GetAvailabilityBuckets(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	checkIdent := httpx.Param(req, "check")

	query := req.URL.Query()

	from, err := parseInstant("from", query.Get("from"))
	if err != nil {
		return h.WriteErrorErr(
			writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid window", err)
	}

	to, err := parseInstant("to", query.Get("to"))
	if err != nil {
		return h.WriteErrorErr(
			writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid window", err)
	}

	var bucket time.Duration

	if raw := query.Get("bucket"); raw != "" {
		bucket, err = time.ParseDuration(raw)
		if err != nil {
			return h.WriteErrorErr(writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Invalid bucket", fmt.Errorf("%w: bucket must be a duration: %w", ErrInvalidWindow, err))
		}
	}

	resp, err := h.svc.GetAvailabilityBuckets(req.Context(), orgSlug, checkIdent,
		&GetAvailabilityBucketsOptions{
			From:   from,
			To:     to,
			Bucket: bucket,
			Region: query.Get("region"),
		})
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		case errors.Is(err, ErrCheckNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
		case errors.Is(err, ErrInvalidWindow):
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid window", err)
		default:
			return h.WriteInternalError(writer, req, err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}
