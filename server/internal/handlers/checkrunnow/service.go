// Package checkrunnow runs a check once on demand, "Run now" (spec
// 2026-10-04-01), and holds the pieces "Capture now" (checkscreenshots) shares
// with it: the job picker, the express hint and the atomic rate-limit
// admission.
//
// Nothing runs here. A run is requested by making the check's job rows due
// and sending the express hint, so the run happens where the check runs: a
// shared cloud worker for a cloud region, the org's agent for a private one.
// The result comes back through the normal result path.
package checkrunnow

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
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// "Run now" rate limits. Cheap types get 3 per check per minute; the types
// that start a browser or a desktop session use the capture limits (see
// CaptureLimits).
const (
	CheckLimit  = 3
	CheckWindow = time.Minute
	OrgLimit    = 60
	OrgWindow   = time.Hour
)

// Region statuses of a RunNowResponse.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
)

// ExpressHintEvent is the notifier channel the check workers' express path
// listens on (in-process: DirectBackend.Hints; deported agents: the WS relay's
// jobs-available frames). It is named after check creation for historical
// reasons; its payload only says "this check has a due job, claim it now".
const ExpressHintEvent = string(models.EventTypeCheckCreated)

// Errors returned by the service.
var (
	// ErrOrganizationNotFound means the org slug resolves to nothing.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrCheckNotFound means the check does not exist in this org.
	ErrCheckNotFound = errors.New("check not found")
	// ErrNoScheduledJob means the check has no job to run (disabled).
	ErrNoScheduledJob = errors.New("the check has no scheduled run (is it disabled?)")
)

// RateLimitedError is a request refused by one of its windows.
type RateLimitedError struct {
	// RetryAfter is how long until the window that refused reopens.
	RetryAfter time.Duration
	// Scope is "check" or "organization".
	Scope string
	// Action names the refused action ("run now"); "capture now" when empty.
	Action string
}

func (e *RateLimitedError) Error() string {
	action := e.Action
	if action == "" {
		action = "capture now"
	}

	return fmt.Sprintf("%s is limited per %s: try again in %d s",
		action, e.Scope, int(e.RetryAfter.Round(time.Second).Seconds()))
}

// Limits is one action's pair of windows: per check and per organization.
type Limits struct {
	Action         string
	CheckKeyPrefix string
	OrgKey         string
	CheckLimit     int
	CheckWindow    time.Duration
	OrgLimit       int
	OrgWindow      time.Duration
}

// Admit counts one request against the check window and the org window,
// atomically and in the database, so the caps hold across API replicas AND
// across concurrent requests: both counters are read and written under row
// locks in one transaction, and a refused request counts against neither.
func Admit(
	ctx context.Context, dbSvc db.Service, orgUID, checkUID string, now time.Time, limits *Limits,
) error {
	windows := []models.FixedWindow{
		{Key: limits.CheckKeyPrefix + checkUID, Limit: limits.CheckLimit, Window: limits.CheckWindow},
		{Key: limits.OrgKey, Limit: limits.OrgLimit, Window: limits.OrgWindow},
	}
	scopes := []string{"check", "organization"}

	refused, retryAfter, err := dbSvc.AdmitFixedWindows(ctx, orgUID, windows, now)
	if err != nil {
		return fmt.Errorf("admit %s: %w", limits.Action, err)
	}

	if refused >= 0 {
		return &RateLimitedError{RetryAfter: retryAfter, Scope: scopes[refused], Action: limits.Action}
	}

	return nil
}

// Hint publishes the express hint for the check. Best-effort: without it the
// due job still runs on the workers' next regular poll.
func Hint(ctx context.Context, eventNotifier notifier.EventNotifier, checkUID string) {
	if eventNotifier == nil {
		return
	}

	payload, err := json.Marshal(map[string]string{"check_uid": checkUID})
	if err != nil {
		return
	}

	if err := eventNotifier.Notify(ctx, ExpressHintEvent, string(payload)); err != nil {
		slog.WarnContext(ctx, "Failed to send the express hint", "checkUid", checkUID, "error", err)
	}
}

// PickJob chooses the job row "Capture now" runs on: one that is not leased
// right now if there is one (it can be claimed at once), in region order so the
// choice is stable; otherwise the first one — the release keeps a pending
// request due, so a leased job only delays the capture by its current run.
func PickJob(jobs []*models.CheckJob, now time.Time) *models.CheckJob {
	if len(jobs) == 0 {
		return nil
	}

	sorted := SortedByRegion(jobs)

	for _, job := range sorted {
		if job.LeaseExpiresAt == nil || job.LeaseExpiresAt.Before(now) {
			return job
		}
	}

	return sorted[0]
}

// SortedByRegion returns a copy of the jobs in region order.
func SortedByRegion(jobs []*models.CheckJob) []*models.CheckJob {
	sorted := slices.Clone(jobs)
	slices.SortStableFunc(sorted, func(a, b *models.CheckJob) int {
		return compareRegion(a.Region, b.Region)
	})

	return sorted
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

// ResolveCheck resolves an org slug and a check uid-or-slug.
func ResolveCheck(ctx context.Context, dbSvc db.Service, orgSlug, identifier string) (*models.Check, error) {
	org, err := dbSvc.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil || org == nil {
		return nil, ErrOrganizationNotFound
	}

	check, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	return check, nil
}

// captureTypes are the check types that start a browser or a desktop session:
// they keep the tight capture limits.
//
//nolint:gochecknoglobals // immutable lookup table
var captureTypes = []string{"browser", "js", "rdp", "vnc"}

// IsCapturableType reports whether a check type can produce a screenshot.
func IsCapturableType(checkType string) bool {
	return slices.Contains(captureTypes, checkType)
}

// LimitsFor returns the "Run now" windows for a check type.
func LimitsFor(checkType string) *Limits {
	limits := &Limits{
		Action:         "run now",
		CheckKeyPrefix: "run-now.check.",
		OrgKey:         "run-now.org",
		CheckLimit:     CheckLimit,
		CheckWindow:    CheckWindow,
		OrgLimit:       OrgLimit,
		OrgWindow:      OrgWindow,
	}

	if IsCapturableType(checkType) {
		limits.CheckLimit = 1
		limits.OrgLimit = 20
	}

	return limits
}

// RegionRun is one region's answer to a run-now request.
type RegionRun struct {
	Region string `json:"region"`
	// Status is "queued" (due now) or "running" (its current run answers).
	Status string `json:"status"`
}

// RunNowResponse is the body of an accepted "Run now".
type RunNowResponse struct {
	// RequestedAt is when the request was recorded. A result with a later
	// periodStart is this request's answer.
	RequestedAt time.Time   `json:"requestedAt"`
	Regions     []RegionRun `json:"regions"`
}

// Service implements "Run now".
type Service struct {
	db       db.Service
	notifier notifier.EventNotifier
	clock    clock.Clock
}

// NewService builds the service. notifier may be nil (no express hint: the due
// jobs are then picked up by the workers' next regular poll).
func NewService(dbSvc db.Service, eventNotifier notifier.EventNotifier, clk clock.Clock) *Service {
	if clk == nil {
		clk = clock.Real{}
	}

	return &Service{db: dbSvc, notifier: eventNotifier, clock: clk}
}

// isRunning reports whether a job is mid-run: leased, or carrying a
// multi-step run in progress (its lease expires between slices).
func isRunning(job *models.CheckJob, now time.Time) bool {
	if job.StepRunUID != nil {
		return true
	}

	return job.LeaseExpiresAt != nil && !job.LeaseExpiresAt.Before(now)
}

func regionOf(job *models.CheckJob) string {
	if job.Region == nil {
		return ""
	}

	return *job.Region
}

func allRunning(jobs []*models.CheckJob, now time.Time) bool {
	for _, job := range jobs {
		if !isRunning(job, now) {
			return false
		}
	}

	return true
}

func runningResponse(jobs []*models.CheckJob, now time.Time) *RunNowResponse {
	resp := &RunNowResponse{RequestedAt: now, Regions: make([]RegionRun, 0, len(jobs))}

	for _, job := range SortedByRegion(jobs) {
		region := regionOf(job)

		resp.Regions = append(resp.Regions, RegionRun{Region: region, Status: StatusRunning})
	}

	return resp
}

// RunNow makes every region of the check run once, now.
//
// Validation runs before admission, so a request that could never run spends
// no budget.
func (s *Service) RunNow(ctx context.Context, orgSlug, identifier string) (*RunNowResponse, error) {
	check, err := ResolveCheck(ctx, s.db, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	if !check.Enabled {
		return nil, ErrNoScheduledJob
	}

	jobs, err := s.db.ListCheckJobsByCheckUID(ctx, check.UID)
	if err != nil {
		return nil, fmt.Errorf("list check jobs: %w", err)
	}

	if len(jobs) == 0 {
		return nil, ErrNoScheduledJob
	}

	// Microsecond precision, the finest both engines store, so the value
	// handed back compares exactly with a result's periodStart.
	now := s.clock.Now().Truncate(time.Microsecond)

	// Nothing to queue: report every region running and spend no budget.
	if allRunning(jobs, now) {
		return runningResponse(jobs, now), nil
	}

	if err := Admit(ctx, s.db, check.OrganizationUID, check.UID, now, LimitsFor(check.Type)); err != nil {
		return nil, err
	}

	resp := &RunNowResponse{RequestedAt: now, Regions: make([]RegionRun, 0, len(jobs))}
	queued := false

	for _, job := range SortedByRegion(jobs) {
		region := regionOf(job)

		status := StatusRunning

		if !isRunning(job, now) {
			switch err := s.db.RequestCheckRun(ctx, job.UID, now); {
			case err == nil:
				status = StatusQueued
				queued = true
			case errors.Is(err, sql.ErrNoRows):
				continue
			case errors.Is(err, db.ErrCheckJobBusy):
				// A worker claimed it between the read and the update.
			default:
				return nil, fmt.Errorf("request run: %w", err)
			}
		}

		resp.Regions = append(resp.Regions, RegionRun{Region: region, Status: status})
	}

	if len(resp.Regions) == 0 {
		return nil, ErrNoScheduledJob
	}

	if queued {
		Hint(ctx, s.notifier, check.UID)
	}

	return resp, nil
}
