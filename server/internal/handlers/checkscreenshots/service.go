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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
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

// expressHintEvent is the notifier channel the check workers' express path
// listens on (in-process: DirectBackend.Hints; deported agents: the WS relay's
// jobs-available frames). It is named after check creation for historical
// reasons; its payload only says "this check has a due job, claim it now".
const expressHintEvent = string(models.EventTypeCheckCreated)

// Errors returned by the service.
var (
	// ErrOrganizationNotFound means the org slug resolves to nothing.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckNotFound means the check does not exist in this org.
	ErrCheckNotFound = errors.New("check not found")
	// ErrNotCapturable means the check type never produces a screenshot.
	ErrNotCapturable = errors.New("only browser and js checks can capture a screenshot")
	// ErrNoScheduledJob means the check has no job to run (disabled).
	ErrNoScheduledJob = errors.New("the check has no scheduled run (is it disabled?)")
)

// RateLimitedError is "Capture now" refused by one of its windows.
type RateLimitedError struct {
	// RetryAfter is how long until the window that refused reopens.
	RetryAfter time.Duration
	// Scope is "check" or "organization".
	Scope string
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("capture now is limited per %s: try again in %d s",
		e.Scope, int(e.RetryAfter.Round(time.Second).Seconds()))
}

// captureTypes are the check types that can produce a screenshot.
//
//nolint:gochecknoglobals // immutable lookup table
var captureTypes = []string{"browser", "js"}

// IsCapturableType reports whether a check type can produce a screenshot.
func IsCapturableType(checkType string) bool {
	return slices.Contains(captureTypes, checkType)
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
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return nil, ErrOrganizationNotFound
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	return check, nil
}

// ListScreenshots returns a check's latest screenshots, newest first. A limit
// outside [1, MaxLimit] is clamped. Any check type answers — a type that never
// captures simply has none.
func (s *Service) ListScreenshots(
	ctx context.Context, orgSlug, identifier string, limit int,
) ([]attachments.CheckScreenshot, error) {
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

	if s.lister == nil {
		return []attachments.CheckScreenshot{}, nil
	}

	return s.lister.ListCheckScreenshots(ctx, check.OrganizationUID, check.UID, limit)
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

	job := pickJob(jobs, s.clock.Now())
	if job == nil {
		return nil, ErrNoScheduledJob
	}

	now := s.clock.Now()

	// Both windows are admitted atomically, or neither: a request the org cap
	// refuses does not burn the check's minute, and concurrent requests can
	// never exceed either cap (see db.Service.AdmitFixedWindows).
	if err := s.admit(ctx, check.OrganizationUID, check.UID, now); err != nil {
		return nil, err
	}

	if err := s.db.RequestCheckCapture(ctx, job.UID, now); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoScheduledJob
		}

		return nil, fmt.Errorf("request capture: %w", err)
	}

	s.hint(ctx, check.UID)

	resp := &CaptureResponse{RequestedAt: now}
	if job.Region != nil {
		resp.Region = *job.Region
	}

	return resp, nil
}

// pickJob chooses the job row "Capture now" runs on: one that is not leased
// right now if there is one (it can be claimed at once), in region order so the
// choice is stable; otherwise the first one — the release keeps a pending
// request due, so a leased job only delays the capture by its current run.
func pickJob(jobs []*models.CheckJob, now time.Time) *models.CheckJob {
	if len(jobs) == 0 {
		return nil
	}

	sorted := slices.Clone(jobs)
	slices.SortStableFunc(sorted, func(a, b *models.CheckJob) int {
		return compareRegion(a.Region, b.Region)
	})

	for _, job := range sorted {
		if job.LeaseExpiresAt == nil || job.LeaseExpiresAt.Before(now) {
			return job
		}
	}

	return sorted[0]
}

func compareRegion(leftRegion, rightRegion *string) int {
	left, right := "", ""
	if leftRegion != nil {
		left = *leftRegion
	}

	if rightRegion != nil {
		right = *rightRegion
	}

	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

// hint publishes the express hint for the check. Best-effort: without it the
// flagged job still runs on the workers' next regular poll.
func (s *Service) hint(ctx context.Context, checkUID string) {
	if s.notifier == nil {
		return
	}

	payload, err := json.Marshal(map[string]string{"check_uid": checkUID})
	if err != nil {
		return
	}

	if err := s.notifier.Notify(ctx, expressHintEvent, string(payload)); err != nil {
		slog.WarnContext(ctx, "Failed to send the capture-now express hint", "checkUid", checkUID, "error", err)
	}
}

// admit counts one "Capture now" against the check window and the org window,
// atomically and in the database — so the caps hold across API replicas AND
// across concurrent requests on one replica: the admission reads and writes
// both counters under row locks in one transaction, and a refused request
// counts against neither window.
func (s *Service) admit(ctx context.Context, orgUID, checkUID string, now time.Time) error {
	windows := []models.FixedWindow{
		{Key: captureCheckKeyPrefix + checkUID, Limit: CaptureCheckLimit, Window: CaptureCheckWindow},
		{Key: captureOrgKey, Limit: CaptureOrgLimit, Window: CaptureOrgWindow},
	}
	scopes := []string{"check", "organization"}

	refused, retryAfter, err := s.db.AdmitFixedWindows(ctx, orgUID, windows, now)
	if err != nil {
		return fmt.Errorf("admit capture: %w", err)
	}

	if refused >= 0 {
		return &RateLimitedError{RetryAfter: retryAfter, Scope: scopes[refused]}
	}

	return nil
}
