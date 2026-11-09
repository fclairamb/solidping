package availability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/uptimebar"
)

// MaxOrgBucketChecks caps how many checks one org-level bucketed request may
// name, matching the dashboard glance card's page clamp.
const MaxOrgBucketChecks = 100

// ErrTooManyChecks is returned when more than MaxOrgBucketChecks UIDs are asked for.
var ErrTooManyChecks = errors.New("too many checks")

// CheckBuckets is one check's cell series in the org-level response.
type CheckBuckets struct {
	Buckets []Bucket `json:"buckets"`
	// LatestDurationMs is the average response time of the newest cell that has
	// one, omitted when no cell does.
	LatestDurationMs *float64 `json:"latestDurationMs,omitempty"`
}

// ListOrgAvailabilityBucketsResponse is the org-level bucketed payload, keyed
// by check UID.
type ListOrgAvailabilityBucketsResponse struct {
	Data          map[string]CheckBuckets `json:"data"`
	BucketSeconds int                     `json:"bucketSeconds"`
	WindowStart   time.Time               `json:"windowStart"`
	WindowEnd     time.Time               `json:"windowEnd"`
}

// GetOrgAvailabilityBucketsOptions carries the parsed request parameters.
type GetOrgAvailabilityBucketsOptions struct {
	From      time.Time
	To        time.Time
	Bucket    time.Duration
	CheckUIDs []string
}

// GetOrgAvailabilityBuckets cuts [from, to) into hour-aligned cells for several
// checks of one org in a single engine call. UIDs that do not belong to the org
// are ignored.
func (s *Service) GetOrgAvailabilityBuckets(
	ctx context.Context, orgSlug string, opts *GetOrgAvailabilityBucketsOptions,
) (*ListOrgAvailabilityBucketsResponse, error) {
	if len(opts.CheckUIDs) > MaxOrgBucketChecks {
		return nil, fmt.Errorf("%w: at most %d checks per request", ErrTooManyChecks, MaxOrgBucketChecks)
	}

	plan, err := planBuckets(opts.From, opts.To, opts.Bucket)
	if err != nil {
		return nil, err
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	resp := &ListOrgAvailabilityBucketsResponse{
		Data:          map[string]CheckBuckets{},
		BucketSeconds: int(plan.width.Seconds()),
		WindowStart:   opts.From.UTC(),
		WindowEnd:     opts.To.UTC(),
	}

	found := map[string]struct{}{}
	if len(opts.CheckUIDs) > 0 {
		checks, getErr := s.db.GetChecksByUIDs(ctx, org.UID, opts.CheckUIDs)
		if getErr != nil {
			return nil, getErr
		}

		for uid := range checks {
			found[uid] = struct{}{}
		}
	}

	if len(found) == 0 {
		return resp, nil
	}

	uids := make([]string, 0, len(found))
	for uid := range found {
		uids = append(uids, uid)
	}

	stats, err := uptimebar.BucketAvailabilityInRegions(
		ctx, s.db, org.UID, uids, nil, plan.width, plan.start, plan.count, s.uptimebarHints(ctx))
	if err != nil {
		return nil, err
	}

	for _, uid := range uids {
		byBucket := stats[uid]
		cells := make([]Bucket, 0, plan.count)

		for i := range plan.count {
			cellStart := plan.start.Add(time.Duration(i) * plan.width)
			cells = append(cells, bucketDTO(byBucket[cellStart], cellStart, cellStart.Add(plan.width)))
		}

		entry := CheckBuckets{Buckets: cells}

		for i := len(cells) - 1; i >= 0; i-- {
			if cells[i].AvgDurationMs != nil {
				entry.LatestDurationMs = cells[i].AvgDurationMs

				break
			}
		}

		resp.Data[uid] = entry
	}

	return resp, nil
}

// parseBucketParam accepts a Go duration ("1h") or a whole number of seconds ("3600").
func parseBucketParam(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}

	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second, nil
	}

	return time.ParseDuration(raw)
}

// GetOrgAvailabilityBuckets handles GET /api/v1/orgs/:org/availability/buckets.
func (h *Handler) GetOrgAvailabilityBuckets(writer http.ResponseWriter, req *http.Request) error {
	query := req.URL.Query()

	from, err := parseInstant("from", query.Get("from"))
	if err != nil {
		return h.WriteErrorErr(
			writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid window", err)
	}

	until, err := parseInstant("to", query.Get("to"))
	if err != nil {
		return h.WriteErrorErr(
			writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid window", err)
	}

	bucket, err := parseBucketParam(query.Get("bucket"))
	if err != nil {
		return h.WriteErrorErr(writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid bucket", fmt.Errorf("%w: bucket must be seconds or a duration: %w", ErrInvalidWindow, err))
	}

	var uids []string

	for _, uid := range strings.Split(query.Get("checks"), ",") {
		if uid = strings.TrimSpace(uid); uid != "" {
			uids = append(uids, uid)
		}
	}

	resp, err := h.svc.GetOrgAvailabilityBuckets(req.Context(), httpx.Param(req, "org"),
		&GetOrgAvailabilityBucketsOptions{From: from, To: until, Bucket: bucket, CheckUIDs: uids})
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		case errors.Is(err, ErrInvalidWindow), errors.Is(err, ErrTooManyChecks):
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid request", err)
		default:
			return h.WriteInternalError(writer, req, err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}
