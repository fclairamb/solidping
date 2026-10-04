// Package checkscreenshots serves a check's screenshots on the check page
// (spec 2026-09-25-34): the listing of its latest captures — the ones its
// incidents carry and the check-scoped ones — and "Capture now", which runs the
// check once on demand with the capture forced.
//
// It is its own package so neither the checks service nor the incidents
// service grows a files dependency: the listing reads through the attachment
// service, and "Capture now" only flags a check_jobs row.
package checkscreenshots

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/checkrunnow"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Listing bounds. Five is what the check card shows; twenty is enough for any
// client and keeps a response full of signed URLs small.
const (
	DefaultLimit = 5
	MaxLimit     = 20
)

// "Capture now" rate limits (spec 2026-09-25-34). A browser run is the most
// expensive check type, so the button is capped per check (one pending
// capture at a time is the realistic use) and per organization (so a script
// looping over every check cannot turn the org's workers into a screenshot
// farm).
const (
	CaptureCheckLimit  = 1
	CaptureCheckWindow = time.Minute
	CaptureOrgLimit    = 20
	CaptureOrgWindow   = time.Hour
)

// State-entry keys for the two windows. Org-scoped entries, so an org's
// deletion takes them along.
const (
	captureCheckKeyPrefix = "capture-now.check."
	captureOrgKey         = "capture-now.org"
)

//nolint:gochecknoglobals // immutable
var captureLimits = checkrunnow.Limits{
	Action:         "capture now",
	CheckKeyPrefix: captureCheckKeyPrefix,
	OrgKey:         captureOrgKey,
	CheckLimit:     CaptureCheckLimit,
	CheckWindow:    CaptureCheckWindow,
	OrgLimit:       CaptureOrgLimit,
	OrgWindow:      CaptureOrgWindow,
}

// Errors returned by the service.
var (
	// ErrOrganizationNotFound means the org slug resolves to nothing.
	ErrOrganizationNotFound = checkrunnow.ErrOrganizationNotFound
	// ErrCheckNotFound means the check does not exist in this org.
	ErrCheckNotFound = checkrunnow.ErrCheckNotFound
	// ErrNotCapturable means the check type never produces a screenshot.
	ErrNotCapturable = errors.New("only browser and js checks can capture a screenshot")
	// ErrNoScheduledJob means the check has no job to run (disabled).
	ErrNoScheduledJob = checkrunnow.ErrNoScheduledJob
)

// RateLimitedError is "Capture now" refused by one of its windows.
type RateLimitedError = checkrunnow.RateLimitedError

// IsCapturableType reports whether a check type can produce a screenshot.
func IsCapturableType(checkType string) bool {
	return checkrunnow.IsCapturableType(checkType)
}

// Lister is the attachment side the listing needs.
type Lister interface {
	ListCheckScreenshots(ctx context.Context, orgUID, checkUID string, limit int) ([]attachments.CheckScreenshot, error)
}

// Service implements the listing and "Capture now".
type Service struct {
	db       db.Service
	lister   Lister
	notifier notifier.EventNotifier
	clock    clock.Clock
}

// NewService builds the service. notifier may be nil (no express hint: the
// flagged job is then picked up by the workers' next regular poll).
func NewService(dbSvc db.Service, lister Lister, eventNotifier notifier.EventNotifier, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real{}
	}

	return &Service{db: dbSvc, lister: lister, notifier: eventNotifier, clock: clk}
}

// CaptureResponse is the body of an accepted "Capture now".
type CaptureResponse struct {
	// Region is where the capture will run.
	Region string `json:"region,omitempty"`
	// RequestedAt is when the request was recorded. A capture that lands in
	// the listing with a later capturedAt is this request's answer.
	RequestedAt time.Time `json:"requestedAt"`
}

// resolveCheck resolves an org slug and a check uid-or-slug.
func (s *Service) resolveCheck(ctx context.Context, orgSlug, identifier string) (*models.Check, error) {
	return checkrunnow.ResolveCheck(ctx, s.db, orgSlug, identifier)
}

// CaptureOutcome is how the latest FAILED "Capture now" request ended (spec
// 2026-09-27-01): a run carried the request and came back without a
// screenshot. The dashboard matches RequestedAt against the requestedAt its
// POST .../capture returned, and stops waiting on a match. A successful
// capture has no outcome here: its image in the listing is the answer.
type CaptureOutcome struct {
	// RequestedAt is the request this outcome answers, exactly as the capture
	// endpoint returned it (stored at the database's precision).
	RequestedAt time.Time `json:"requestedAt"`
	// Failed is always true today; it is spelled out so a later "succeeded"
	// outcome can be added without changing the shape.
	Failed bool `json:"failed"`
	// Error says why the run produced no screenshot.
	Error string `json:"error,omitempty"`
}

// Listing is a check's latest screenshots plus the outcome of its latest
// failed "Capture now" request, if any.
type Listing struct {
	Screenshots    []attachments.CheckScreenshot
	CaptureOutcome *CaptureOutcome
}

// ListScreenshots returns a check's latest screenshots, newest first, and its
// latest failed "Capture now" request. A limit outside [1, MaxLimit] is
// clamped. Any check type answers — a type that never captures simply has
// none.
func (s *Service) ListScreenshots(
	ctx context.Context, orgSlug, identifier string, limit int,
) (*Listing, error) {
	check, err := s.resolveCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}

	listing := &Listing{Screenshots: []attachments.CheckScreenshot{}}

	if s.lister != nil {
		shots, listErr := s.lister.ListCheckScreenshots(ctx, check.OrganizationUID, check.UID, limit)
		if listErr != nil {
			return nil, listErr
		}

		if shots != nil {
			listing.Screenshots = shots
		}
	}

	if IsCapturableType(check.Type) {
		jobs, jobsErr := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
		if jobsErr != nil {
			return nil, fmt.Errorf("list check jobs: %w", jobsErr)
		}

		listing.CaptureOutcome = latestCaptureFailure(jobs)
	}

	return listing, nil
}

// latestCaptureFailure picks the newest failed "Capture now" request across a
// check's job rows (a multi-region check has one per region, and the request
// lands on one of them).
func latestCaptureFailure(jobs []*models.CheckJob) *CaptureOutcome {
	var latest *CaptureOutcome

	for _, job := range jobs {
		if job.CaptureFailedRequestAt == nil {
			continue
		}

		if latest != nil && !job.CaptureFailedRequestAt.After(latest.RequestedAt) {
			continue
		}

		latest = &CaptureOutcome{RequestedAt: *job.CaptureFailedRequestAt, Failed: true}
		if job.CaptureFailureReason != nil {
			latest.Error = *job.CaptureFailureReason
		}
	}

	return latest
}

// CaptureNow runs the check once on demand with the capture forced (spec
// 2026-09-25-34).
//
// It goes through the check's own scheduling rather than running anything
// here, so the run happens where the check runs — a shared cloud worker for a
// cloud region, the org's agent for a private one: one job row is flagged
// (capture_requested_at) and made due, and the express hint tells the workers
// to claim it now. The capture then arrives through the normal result path and
// lands under the check-scoped topic.
//
// Validation runs before the rate limiter, so a request that could never run
// spends no budget.
func (s *Service) CaptureNow(ctx context.Context, orgSlug, identifier string) (*CaptureResponse, error) {
	check, err := s.resolveCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	if !IsCapturableType(check.Type) {
		return nil, ErrNotCapturable
	}

	jobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return nil, fmt.Errorf("list check jobs: %w", err)
	}

	job := checkrunnow.PickJob(jobs, s.clock.Now())
	if job == nil {
		return nil, ErrNoScheduledJob
	}

	// Microsecond precision, the finest both engines store: the requestedAt
	// returned below is then exactly the value a failed outcome is recorded
	// under (CaptureOutcome.RequestedAt), so the dashboard can match the two.
	now := s.clock.Now().Truncate(time.Microsecond)

	// Both windows are admitted atomically, or neither: a request the org cap
	// refuses does not burn the check's minute, and concurrent requests can
	// never exceed either cap (see db.Service.AdmitFixedWindows).
	if err := checkrunnow.Admit(ctx, s.db, check.OrganizationUID, check.UID, now, captureLimits); err != nil {
		return nil, err
	}

	if err := s.db.RequestCheckCapture(ctx, job.UID, now); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoScheduledJob
		}

		return nil, fmt.Errorf("request capture: %w", err)
	}

	checkrunnow.Hint(ctx, s.notifier, check.UID)

	resp := &CaptureResponse{RequestedAt: now}
	if job.Region != nil {
		resp.Region = *job.Region
	}

	return resp, nil
}
