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

// Keys of the fixed-window counter stored in a state entry's value.
const (
	windowKeyCount = "count"
	windowKeyStart = "windowStart"
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
	orgUID := check.OrganizationUID

	// Both windows are checked before either is spent, so a request the org
	// cap refuses does not also burn the check's minute.
	checkWindow, err := s.window(ctx, orgUID, captureCheckKeyPrefix+check.UID,
		CaptureCheckLimit, CaptureCheckWindow, "check", now)
	if err != nil {
		return nil, err
	}

	orgWindow, err := s.window(ctx, orgUID, captureOrgKey, CaptureOrgLimit, CaptureOrgWindow, "organization", now)
	if err != nil {
		return nil, err
	}

	for _, spend := range []func() error{checkWindow, orgWindow} {
		if err := spend(); err != nil {
			return nil, err
		}
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

// window reads one fixed window stored in a state entry and either refuses the
// request (RateLimitedError, with the time until the window reopens) or returns
// the function that counts it.
//
// DB-backed rather than an in-memory limiter so the cap holds across API
// replicas — an in-process bucket would multiply by the replica count. The
// read-modify-write is not atomic; two requests racing on the same window can
// both pass, which over-admits by at most the number of replicas and is
// harmless for a cap whose purpose is keeping a button from being hammered.
func (s *Service) window(
	ctx context.Context, orgUID, key string, limit int, window time.Duration, scope string, now time.Time,
) (func() error, error) {
	entry, err := s.db.GetStateEntry(ctx, &orgUID, key)
	if err != nil {
		return nil, fmt.Errorf("read capture window: %w", err)
	}

	count, start := readWindow(entry)
	if start.IsZero() || !now.Before(start.Add(window)) {
		count, start = 0, now
	}

	if count >= limit {
		return nil, &RateLimitedError{RetryAfter: start.Add(window).Sub(now), Scope: scope}
	}

	return func() error {
		value := models.JSONMap{
			windowKeyCount: count + 1,
			windowKeyStart: start.UTC().Format(time.RFC3339Nano),
		}

		ttl := start.Add(window).Sub(now)
		if ttl < time.Second {
			ttl = time.Second
		}

		if err := s.db.SetStateEntry(ctx, &orgUID, key, &value, &ttl); err != nil {
			return fmt.Errorf("write capture window: %w", err)
		}

		return nil
	}, nil
}

// readWindow decodes a fixed-window counter; a missing or unreadable entry is
// an empty window.
func readWindow(entry *models.StateEntry) (int, time.Time) {
	if entry == nil || entry.Value == nil {
		return 0, time.Time{}
	}

	value := *entry.Value

	startRaw, ok := value[windowKeyStart].(string)
	if !ok {
		return 0, time.Time{}
	}

	start, err := time.Parse(time.RFC3339Nano, startRaw)
	if err != nil {
		return 0, time.Time{}
	}

	switch count := value[windowKeyCount].(type) {
	case float64:
		return int(count), start
	case int:
		return count, start
	case int64:
		return int(count), start
	default:
		return 0, start
	}
}
