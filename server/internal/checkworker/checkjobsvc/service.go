// Package checkjobsvc provides check job queue operations for the distributed check runner system.
package checkjobsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// ErrJobClaimedByAnother is returned when a job has been claimed by another worker.
var ErrJobClaimedByAnother = errors.New("job may have been claimed by another worker")

// Service provides check job queue operations.
type Service interface {
	// ClaimJobs atomically claims due check jobs for the given worker with
	// per-lane reservation (spec 2026-07-01-03 D3/D6). fastLimit is the total
	// claim capacity (normally the worker's free runner slots). slowLimit is
	// the slow lane's reservation budget: a worker passing slowLimit =
	// (poolSize − fastLaneReserved) − busySlow guarantees slow jobs can never
	// occupy the reserved fast floor. The slow lane claims its budgeted share
	// FIRST (capped at min(slowLimit, fastLimit)); fast then fills every
	// remaining slot. Slow-first is load-bearing: fast claim-ahead keeps the
	// fast lane permanently eligible on fleets whose periods fit inside
	// maxAhead, so a leftovers-only slow allowance would be zero forever and
	// starve the slow lane outright. Both SELECTs run in one transaction.
	// Lease duration is calculated per job as scheduled_at + period + 30s.
	// Returns claimed jobs (nil if none available) plus a next-eligible hint:
	// how long until the earliest still-unleased job in scope becomes
	// claimable (0 = none within the hint horizon). The worker's fetcher
	// sleeps on that hint instead of a flat fallback poll, which is what
	// keeps sub-minute periods honest on an otherwise idle worker.
	ClaimJobs(
		ctx context.Context,
		workerUID string,
		region *string,
		fastLimit int,
		slowLimit int,
		maxAhead time.Duration,
	) ([]*models.CheckJob, time.Duration, error)

	// ClaimJobsForCheck atomically claims any due check_jobs rows for the
	// given checkUID. Used by the express runner that wakes up on
	// check.created events and bypasses the regular runner pool so a
	// freshly-created check produces its first real result without
	// queueing behind in-flight long-running checks.
	ClaimJobsForCheck(
		ctx context.Context,
		workerUID string,
		region *string,
		checkUID string,
	) ([]*models.CheckJob, error)

	// ClaimJobsForAgent atomically claims due jobs for a deported agent
	// (spec 2026-07-16-02, generalized by 2026-07-27-01). The scope is HARD in
	// both directions: the region always matches on exact equality — no prefix
	// matching, no NULL-region fallback — and an org agent additionally matches
	// exactly its own organization, so a compromised or misconfigured
	// tenant-private agent can never claim another org's or another region's
	// work. A system (platform-operated) agent serves a SHARED cloud region and
	// therefore claims that region across all orgs, exactly like the in-cluster
	// DirectBackend; see AgentScope, which fails closed on an ambiguous scope.
	//
	// checkUID, when non-empty, further pins the claim to one check (the agent
	// express path). No lane split applies (an agent pool is not the server's
	// fast/slow reservation); ordering stays cost-aware via
	// effective_scheduled_at. The second return is the same next-eligible hint
	// as ClaimJobs, computed over the agent's whole scope regardless of any
	// checkUID pin, so the agent's fetcher always learns when to come back.
	ClaimJobsForAgent(
		ctx context.Context,
		workerUID string,
		scope AgentScope,
		checkUID string,
		limit int,
		maxAhead time.Duration,
	) ([]*models.CheckJob, time.Duration, error)

	// ReleaseLease releases the lease and reschedules the job for next execution.
	// It folds in no fresh cost/delay sample (the probe was skipped, or ran on a
	// backend that does not measure cost), so it re-anchors effective_scheduled_at
	// to the new schedule; the cost offset is reapplied on the next
	// post-exec write via ReleaseLeaseWithSchedulingState.
	//
	// This is the *terminal* release: the attempt is over (an error result was
	// written, or the reaper gave up on a stuck job) and the job starts its next
	// period with a clean slate. A job that was merely DEFERRED — the per-org
	// rate limiter turned it away before the probe ran — must use
	// DeferLeaseRateLimited instead, or it loses the overdue-ness that earns it
	// priority next window.
	ReleaseLease(ctx context.Context, jobUID string, workerUID string, nextScheduledAt time.Time) error

	// DeferLeaseRateLimited releases the lease of a job the per-org
	// MaxChecksPerMinute gate turned away before it ran, advancing scheduled_at
	// to the next tick but DELIBERATELY PRESERVING effective_scheduled_at
	// (spec 2026-08-26-02).
	//
	// That one omitted assignment is the whole fairness mechanism. Claim order
	// is `ORDER BY effective_scheduled_at ASC`, so a job whose ordering key
	// stays pinned at the tick it missed keeps receding relative to now and
	// sorts ahead of its on-time org siblings in the next window. An org over
	// its cap therefore rotates the deficit round-robin instead of starving the
	// same deterministic-phase losers forever. Re-anchoring here (what
	// ReleaseLease does) is what let a 1-minute check go 7.5 hours without a
	// single execution while its org ran at full rate.
	//
	// No busy-loop risk: the claim predicate still gates on
	// `scheduled_at <= now + ahead`, so a deferred job cannot be re-claimed
	// inside the window it was just turned away from.
	//
	// cost_ewma_ms, delay_ewma_ms and lane are left untouched as well: the probe
	// never ran, so there is no sample to fold in, and the delay EWMA is the
	// diagnostic that exposed this pathology in production.
	DeferLeaseRateLimited(ctx context.Context, jobUID string, workerUID string, nextScheduledAt time.Time) error

	// ReleaseLeaseWithSchedulingState releases the lease, reschedules, and folds
	// the post-exec cost and delay signals into the row in the same write: it
	// stores the updated cost and delay EWMAs, the recomputed
	// effective_scheduled_at used for cost-aware, plan-weighted claim ordering
	// (spec 2026-06-30-09), and the hysteresis-classified lane (spec
	// 2026-07-01-03). No extra query on the hot path — it reuses the single
	// post-exec UPDATE. Callers without a fresh sample (rate-limit deferral,
	// reaper, remote backends) use ReleaseLease instead, which leaves the lane
	// unchanged.
	ReleaseLeaseWithSchedulingState(
		ctx context.Context,
		jobUID string,
		workerUID string,
		nextScheduledAt time.Time,
		costEWMAMs float64,
		delayEWMAMs float64,
		effectiveScheduledAt time.Time,
		lane uint8,
	) error
}

// serviceImpl implements the Service interface.
type serviceImpl struct {
	db *bun.DB
}

// NewService creates a new check job service.
func NewService(db *bun.DB) Service {
	return &serviceImpl{db: db}
}

// ClaimJobs atomically claims check jobs using lease mechanism, one
// lane-filtered SELECT per lane inside a single transaction (spec
// 2026-07-01-03 D3/D6): fast first up to fastLimit (the total capacity), then
// slow up to min(slowLimit, fastLimit − claimed fast). Each SELECT is
// LIMIT-bounded and backed by its lane's partial index. Lease duration is
// calculated per job as scheduled_at + period + 30s. Uses SELECT FOR UPDATE
// SKIP LOCKED on PostgreSQL for efficient row-level locking; optimistic
// locking on SQLite. SKIP LOCKED racing can only under-claim a lane, never
// breach the caller's slow cap.
func (s *serviceImpl) ClaimJobs(
	ctx context.Context,
	workerUID string,
	region *string,
	fastLimit int,
	slowLimit int,
	maxAhead time.Duration,
) ([]*models.CheckJob, time.Duration, error) {
	var jobs []*models.CheckJob
	var nextIn time.Duration
	now := time.Now()
	claimStart := time.Now()

	// Check if database is PostgreSQL
	_, isPostgres := s.db.Dialect().(*pgdialect.Dialect)

	cloudScope := func(query *bun.SelectQuery) *bun.SelectQuery {
		return applyCloudRegionScope(query, region)
	}

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		jobs = nil
		nextIn = 0

		// Slow lane FIRST, capped by its reservation budget and the free
		// capacity. Order matters: with FetchMaxAhead-style claim-ahead, a
		// fleet whose fast periods fit inside the window keeps the fast lane
		// permanently eligible, so a fast-first claim fills every free slot
		// and a leftovers-only slow allowance computes to zero forever —
		// total slow-lane starvation (observed live: zero slow claims while
		// 41 slow jobs sat 11h overdue). Claiming slow up to its budget first
		// cannot starve fast: slowLimit is already bounded by
		// (poolSize − fastReserved) − busySlow, so the reserved fast floor is
		// untouchable regardless of claim order.
		slowN := slowLimit
		if slowN > fastLimit {
			slowN = fastLimit
		}
		if slowN > 0 {
			var slowJobs []*models.CheckJob
			if err := s.selectAvailableJobs(
				ctx, tx, &slowJobs, region, scheduling.LaneSlow, slowN, maxAhead, now, isPostgres,
			); err != nil {
				return err
			}
			jobs = slowJobs
		}

		// Fast lane fills every remaining free slot (all of them when the
		// slow lane is idle — the reservation stays work-conserving).
		fastN := fastLimit - len(jobs)
		if fastN > 0 {
			var fastJobs []*models.CheckJob
			if err := s.selectAvailableJobs(
				ctx, tx, &fastJobs, region, scheduling.LaneFast, fastN, maxAhead, now, isPostgres,
			); err != nil {
				return err
			}
			jobs = append(jobs, fastJobs...)
		}

		if len(jobs) > 0 {
			// Update each job with lease info
			if err := s.updateJobsWithLease(ctx, tx, jobs, workerUID, now, isPostgres); err != nil {
				return err
			}

			// Attach each job's check so the incident hot path can skip a
			// per-result GetCheck. Same tx so claim + fetch are one round-trip pair.
			if err := attachChecks(ctx, tx, jobs); err != nil {
				return err
			}
		}

		// The hint runs AFTER the lease writes so jobs claimed in this very
		// batch (their scheduled_at may still be up to claim-ahead in the
		// future) are excluded by the lease filter instead of producing an
		// immediate wasted re-poll.
		var hintErr error
		nextIn, hintErr = s.nextEligibleIn(ctx, tx, now, maxAhead, cloudScope)

		return hintErr
	})
	prommetrics.RecordCheckStage("claim", time.Since(claimStart).Seconds())

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nextIn, nil // No jobs available
		}

		return nil, 0, err
	}

	return jobs, nextIn, nil
}

// expressClaimLimit caps how many rows the express path may claim per
// check.created event. A check has at most one job per region; 4 leaves
// headroom for multi-region checks without unbounded scope.
const expressClaimLimit = 4

// ClaimJobsForCheck claims any due check_jobs rows for a specific check
// without consulting the rest of the queue. Reuses the same select +
// lease-update plumbing as ClaimJobs so lease semantics, lease_starts
// counting and SKIP LOCKED behavior are identical.
func (s *serviceImpl) ClaimJobsForCheck(
	ctx context.Context,
	workerUID string,
	region *string,
	checkUID string,
) ([]*models.CheckJob, error) {
	var jobs []*models.CheckJob
	now := time.Now()

	_, isPostgres := s.db.Dialect().(*pgdialect.Dialect)

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.selectAvailableJobsForCheck(
			ctx, tx, &jobs, region, checkUID, now, isPostgres,
		); err != nil {
			return err
		}

		if len(jobs) == 0 {
			return nil
		}

		if err := s.updateJobsWithLease(ctx, tx, jobs, workerUID, now, isPostgres); err != nil {
			return err
		}

		return attachChecks(ctx, tx, jobs)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	return jobs, nil
}

// AgentScope is the claim scope of one enrolled agent. It exists so the
// widening a system agent needs (serving a shared cloud region across every
// org) can never happen by accident: an org agent's scope carries its OrgUID,
// and dropping the organization predicate requires System to be set explicitly.
type AgentScope struct {
	// OrgUID pins the claim to exactly one organization. It is empty ONLY for a
	// system agent.
	OrgUID string
	// Region is matched on exact equality for both kinds — never a prefix,
	// never a NULL-region fallback.
	Region string
	// System marks a platform-operated agent, whose scope is the region alone
	// (the same scope the in-cluster DirectBackend claims with). Tenant-private
	// agents leave it false.
	System bool
}

// ErrInvalidAgentScope is returned when a claim scope is not a scope any agent
// can legitimately have. It fails the claim closed rather than widening it.
var ErrInvalidAgentScope = errors.New("invalid agent claim scope")

// validate fails closed on every scope that is not one of the two legitimate
// shapes: an org agent MUST carry an org, and a system agent must serve a real
// (non-private) cloud region — a system scope pointed at an `@org/region` slug
// would be a cross-tenant read, so it is rejected outright.
func (s AgentScope) validate() error {
	if s.Region == "" {
		return fmt.Errorf("%w: empty region", ErrInvalidAgentScope)
	}

	if s.System {
		if s.OrgUID != "" {
			return fmt.Errorf("%w: a system agent has no organization", ErrInvalidAgentScope)
		}

		if strings.HasPrefix(s.Region, privateRegionPrefix) {
			return fmt.Errorf("%w: system agents may not serve private region %q", ErrInvalidAgentScope, s.Region)
		}

		return nil
	}

	if s.OrgUID == "" {
		return fmt.Errorf("%w: org agents must be scoped to an organization", ErrInvalidAgentScope)
	}

	return nil
}

// apply adds the scope's predicates to a check_jobs query. A system scope
// deliberately omits the organization predicate — that IS the generalization.
func (s AgentScope) apply(query *bun.SelectQuery) *bun.SelectQuery {
	if !s.System {
		query = query.Where("organization_uid = ?", s.OrgUID)
	}

	return query.Where("region = ?", s.Region)
}

// privateRegionPrefix mirrors regions.PrivateRegionPrefix. It is duplicated as a
// single character rather than imported so this low-level claim package keeps
// depending on nothing but models (regions pulls in the whole db service).
const privateRegionPrefix = "@"

// applyCloudRegionScope adds the region predicate for the CLOUD claim lane (the
// in-process worker and its express variant). Two independent clauses, both
// load-bearing:
//
//   - private-region jobs are excluded OUTRIGHT. Since spec 2026-08-13-01 a
//     private region is stored org-relatively (`@aws-paris`), so it is unique
//     only WITHIN an org — and this lane deliberately carries no organization
//     predicate, because a cloud region is shared across every org. Excluding
//     `@…` in SQL is therefore what keeps the cloud lane out of tenant-private
//     work, rather than relying on the prefix match happening to fail. It also
//     closes the case of a worker with no configured region at all, which used
//     to drop the region predicate entirely and claim everything.
//   - within what remains, a NULL region means "any region" and a non-NULL one
//     prefix-matches the worker (SP_REGION=eu-fr-paris claims region=eu-fr).
func applyCloudRegionScope(query *bun.SelectQuery, region *string) *bun.SelectQuery {
	query = query.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.
			WhereOr("region IS NULL").
			WhereOr("region NOT LIKE ?", privateRegionPrefix+"%")
	})

	if region == nil {
		return query
	}

	return query.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.
			WhereOr("region IS NULL").
			WhereOr("? LIKE region || '%'", *region)
	})
}

// ClaimJobsForAgent claims due jobs hard-scoped by the agent's scope.
// See the interface doc — this is the deported-agent claim path and its scope
// is a security boundary, not a routing convenience.
func (s *serviceImpl) ClaimJobsForAgent(
	ctx context.Context,
	workerUID string,
	scope AgentScope,
	checkUID string,
	limit int,
	maxAhead time.Duration,
) ([]*models.CheckJob, time.Duration, error) {
	if err := scope.validate(); err != nil {
		return nil, 0, err
	}

	var jobs []*models.CheckJob
	var nextIn time.Duration

	now := time.Now()

	_, isPostgres := s.db.Dialect().(*pgdialect.Dialect)

	coarseAhead := maxAhead
	if coarseAhead > maxClaimAheadFloor {
		coarseAhead = maxClaimAheadFloor
	}

	// The hint deliberately ignores any checkUID pin: the agent's fetcher
	// wants to know when ANY of its jobs becomes claimable, even when this
	// particular claim was an express one pinned to a single check. It uses the
	// same scope as the claim, so a system agent's hint spans orgs too.
	agentScope := scope.apply

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		jobs = nil
		nextIn = 0

		var selErr error
		jobs, selErr = selectAvailableJobsForAgent(
			ctx, tx, scope, checkUID, limit, maxAhead, coarseAhead, now, isPostgres,
		)
		if selErr != nil {
			return selErr
		}

		if len(jobs) > 0 {
			if err := s.updateJobsWithLease(ctx, tx, jobs, workerUID, now, isPostgres); err != nil {
				return err
			}

			if err := attachChecks(ctx, tx, jobs); err != nil {
				return err
			}
		}

		// After the lease writes, so this batch's claims are excluded (same
		// ordering rationale as ClaimJobs).
		var hintErr error
		nextIn, hintErr = s.nextEligibleIn(ctx, tx, now, maxAhead, agentScope)

		return hintErr
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nextIn, nil
		}

		return nil, 0, err
	}

	return jobs, nextIn, nil
}

// selectAvailableJobsForAgent runs the agent claim's SELECT: hard-scoped by the
// agent's scope — always exact region (never NULL-region, never a prefix match,
// structurally the inverse of the cloud-worker path), plus the exact org for a
// tenant-private agent — optionally pinned to one check, then narrowed by the
// same per-job claim-ahead clamp as the in-process path (spec 2026-07-05-08 D3).
func selectAvailableJobsForAgent(
	ctx context.Context,
	tx bun.Tx,
	scope AgentScope,
	checkUID string,
	limit int,
	maxAhead time.Duration,
	coarseAhead time.Duration,
	now time.Time,
	isPostgres bool,
) ([]*models.CheckJob, error) {
	var jobs []*models.CheckJob

	query := scope.apply(tx.NewSelect().Model(&jobs)).
		Where("scheduled_at <= ?", now.Add(coarseAhead)).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				WhereOr("lease_expires_at IS NULL").
				WhereOr("lease_expires_at < ?", now)
		}).
		OrderExpr("effective_scheduled_at ASC").
		Limit(limit)

	if checkUID != "" {
		query = query.Where("check_uid = ?", checkUID)
	}

	if isPostgres {
		query = query.For("UPDATE SKIP LOCKED")
	}

	if err := query.Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, err
	}

	filtered := jobs[:0]

	for _, job := range jobs {
		window := clampAhead(maxAhead, job.Period)
		if job.ScheduledAt == nil || !job.ScheduledAt.After(now.Add(window)) {
			filtered = append(filtered, job)
		}
	}

	return filtered, nil
}

// attachChecks fetches every claimed job's check in one batched SELECT inside
// the claim transaction and stitches each *models.Check onto job.Check. This
// amortizes the per-result GetCheck the incident hot path used to issue. A
// missing check (deleted between scheduling and claim) simply leaves job.Check
// nil; the worker falls back to GetCheck for that case. A scan failure is not
// fatal — leaving every Check nil degrades to the old per-result fetch path.
func attachChecks(ctx context.Context, tx bun.Tx, jobs []*models.CheckJob) error {
	checkUIDs := make([]string, 0, len(jobs))
	seen := make(map[string]struct{}, len(jobs))
	for _, j := range jobs {
		if _, ok := seen[j.CheckUID]; ok {
			continue
		}
		seen[j.CheckUID] = struct{}{}
		checkUIDs = append(checkUIDs, j.CheckUID)
	}

	var checks []*models.Check
	if err := tx.NewSelect().
		Model(&checks).
		Where("uid IN (?)", bun.List(checkUIDs)).
		Where("deleted_at IS NULL").
		Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return fmt.Errorf("failed to batch-fetch checks for claimed jobs: %w", err)
	}

	byUID := make(map[string]*models.Check, len(checks))
	for _, c := range checks {
		byUID[c.UID] = c
	}

	for _, j := range jobs {
		j.Check = byUID[j.CheckUID]
	}

	return nil
}

// selectAvailableJobsForCheck mirrors selectAvailableJobs but pins the
// query to a single check_uid and ignores the maxAhead window — express
// claims only fire for events about a check that is, by definition,
// already due.
func (s *serviceImpl) selectAvailableJobsForCheck(
	ctx context.Context,
	tx bun.Tx,
	jobs *[]*models.CheckJob,
	region *string,
	checkUID string,
	now time.Time,
	isPostgres bool,
) error {
	query := tx.NewSelect().
		Model(jobs).
		Where("check_uid = ?", checkUID).
		Where("scheduled_at <= ?", now).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				WhereOr("lease_expires_at IS NULL").
				WhereOr("lease_expires_at < ?", now)
		}).
		Order("scheduled_at ASC").
		Limit(expressClaimLimit)

	query = applyCloudRegionScope(query, region)

	if isPostgres {
		query = query.For("UPDATE SKIP LOCKED")
	}

	if err := query.Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		return err
	}

	return nil
}

// maxClaimAheadFloor is the hard ceiling on how far ahead of "now" a claimed
// job may sleep in-runner before its scheduled_at (spec 2026-07-05-08 D3).
// Claim-ahead is what makes firing punctual (a runner claims slightly early
// and sleeps the remainder in-slot rather than missing the tick entirely),
// but the historical flat FetchMaxAhead default (5 minutes) let it consume
// most of the pool: with many short-period jobs (e.g. a check running per
// region every minute, one job per region — spec 2026-07-20-05), nearly every
// job is "due within 5 minutes" at any instant, so claim-ahead alone parked
// ~22 of 25 runners. Bounding it per job to at most half its own period (and
// never more than this floor) keeps the parked count near actual poll churn.
const maxClaimAheadFloor = 30 * time.Second

// clampAhead returns the eligibility window for one job: the smaller of the
// caller's maxAhead (the configured FetchMaxAhead), half the job's own
// period, and maxClaimAheadFloor. A job with a short period (e.g. a 1-minute
// check, now one full-period job per region — spec 2026-07-20-05) never parks
// for longer than it actually needs to bridge between polls; a job with a long
// period (e.g. 1 hour) is still bounded by the flat floor so a single
// slow-period job can't occupy a runner slot for minutes either.
//
// period <= 0 degrades to maxAhead unclamped (defensive only — check_jobs
// always carries a positive period).
func clampAhead(maxAhead time.Duration, period timeutils.Duration) time.Duration {
	window := maxAhead
	if window > maxClaimAheadFloor {
		window = maxClaimAheadFloor
	}

	p := time.Duration(period)
	if p > 0 && p/2 < window {
		window = p / 2
	}

	return window
}

// nextEligibleHorizon bounds how far ahead nextEligibleIn looks. The fetcher
// falls back to a flat poll (one minute) when no hint is returned, so a job
// whose eligibility is further out than fallback + the widest possible
// claim-ahead window (maxClaimAheadFloor) can never need a hint-driven wake —
// the fallback poll reaches it first.
const nextEligibleHorizon = time.Minute + maxClaimAheadFloor

// nextEligibleScanLimit caps the hint scan. Eligibility is scheduled_at minus
// a per-job window in [0, maxClaimAheadFloor], so scanning the first rows by
// scheduled_at can miss the true minimum by at most maxClaimAheadFloor — an
// error far below the flat fallback the hint replaces.
const nextEligibleScanLimit = 50

// minNextEligibleIn floors the hint so a job that is already claimable (e.g.
// clipped from this claim by its limit) yields a short positive wait instead
// of a zero/negative one that would spin the fetcher.
const minNextEligibleIn = 100 * time.Millisecond

// nextEligibleIn returns how long until the earliest still-unleased job in
// scope becomes claimable (its scheduled_at minus its per-job claim-ahead
// window), or 0 when no such job exists within nextEligibleHorizon. This is
// the fix for sub-minute periods on an idle worker (spec 2026-07-20-03): the
// fetcher's only periodic wake used to be a flat one-minute poll, and a 10s
// job — claimable only 5s (period/2) before it is due — was never claimable
// at the moment a completion woke the fetcher, so it ran once per minute.
// Scope is injected because the cloud claim (NULL/prefix region match) and
// the agent claim (hard org+region) gate rows differently.
func (s *serviceImpl) nextEligibleIn(
	ctx context.Context,
	tx bun.Tx,
	now time.Time,
	maxAhead time.Duration,
	scope func(*bun.SelectQuery) *bun.SelectQuery,
) (time.Duration, error) {
	var upcoming []*models.CheckJob

	query := tx.NewSelect().
		Model(&upcoming).
		Column("scheduled_at", "period").
		Where("scheduled_at > ?", now).
		Where("scheduled_at <= ?", now.Add(nextEligibleHorizon)).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				WhereOr("lease_expires_at IS NULL").
				WhereOr("lease_expires_at < ?", now)
		}).
		Order("scheduled_at ASC").
		Limit(nextEligibleScanLimit)

	if err := scope(query).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}

		return 0, err
	}

	var best time.Duration

	for _, job := range upcoming {
		if job.ScheduledAt == nil {
			continue
		}

		eligibleIn := job.ScheduledAt.Sub(now) - clampAhead(maxAhead, job.Period)
		if eligibleIn < minNextEligibleIn {
			eligibleIn = minNextEligibleIn
		}

		if best == 0 || eligibleIn < best {
			best = eligibleIn
		}
	}

	return best, nil
}

// selectAvailableJobs builds and executes the query to select available jobs
// in one lane. The lane filter matches the partial-index predicates of
// migration 009 (idx_check_jobs_claim_fast / _slow), so each lane's ordered
// scan stays index-backed.
//
// The claim-ahead window is bounded per job (spec 2026-07-05-08 D3): period
// is stored as a Postgres interval / SQLite text via timeutils.Duration, not
// a plain numeric column, so an exact per-row SQL comparison against
// min(maxAhead, period/2, 30s) would need dialect-specific interval
// arithmetic on both backends. Instead the SQL gate uses the coarser
// min(maxAhead, 30s) bound (tightening the historical flat 5-minute default
// for the common case on its own), and a Go-side post-filter immediately
// after Scan drops any row whose own tighter per-job clamp isn't satisfied
// yet — those rows were only SELECTed (and, on Postgres, row-locked for the
// transaction's duration by FOR UPDATE SKIP LOCKED) but are removed from the
// slice before the caller leases anything, so they are never claimed; the
// next poll picks them up once they're within their own window.
func (s *serviceImpl) selectAvailableJobs(
	ctx context.Context,
	tx bun.Tx,
	jobs *[]*models.CheckJob,
	region *string,
	lane uint8,
	limit int,
	maxAhead time.Duration,
	now time.Time,
	isPostgres bool,
) error {
	coarseAhead := maxAhead
	if coarseAhead > maxClaimAheadFloor {
		coarseAhead = maxClaimAheadFloor
	}

	query := tx.NewSelect().
		Model(jobs).
		// Cost-aware, plan-weighted claim (spec 2026-06-30-09 D2/Option A, gate
		// restored by spec 2026-07-01-02 D3): the eligibility gate is the real
		// scheduled_at — a due job is claimable immediately, no matter what its
		// stored effective_scheduled_at says — while the ordering key is
		// effective_scheduled_at (scheduled_at + bounded cost offset −
		// tier_credit), so deprioritization decides who wins a contended slot but
		// can never strand a job. The offset is clamped to MaxDeprioritizeOffset
		// (60s ≪ maxAhead), so no second gate on the effective time is needed.
		// scheduled_at is also what the Go runner sleeps until, so a job claimed
		// within the maxAhead window still fires on schedule. WFQ ordering
		// applies within the lane; the lane split itself is hard isolation
		// layered on top (spec 2026-07-01-03 D1).
		Where("lane = ?", lane).
		Where("scheduled_at <= ?", now.Add(coarseAhead)).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				WhereOr("lease_expires_at IS NULL").
				WhereOr("lease_expires_at < ?", now)
		}).
		OrderExpr("effective_scheduled_at ASC").
		Limit(limit)

	query = applyCloudRegionScope(query, region)

	// PostgreSQL: Use FOR UPDATE SKIP LOCKED for efficient row-level locking
	if isPostgres {
		query = query.For("UPDATE SKIP LOCKED")
	}

	if err := query.Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // No jobs available
		}
		return err
	}

	// Per-job clamp (D3): drop any row whose own bound (min(maxAhead,
	// period/2, 30s)) is tighter than the coarse SQL gate just applied.
	filtered := (*jobs)[:0]

	for _, job := range *jobs {
		window := clampAhead(maxAhead, job.Period)
		if job.ScheduledAt == nil || !job.ScheduledAt.After(now.Add(window)) {
			filtered = append(filtered, job)
		}
	}

	*jobs = filtered

	return nil
}

// updateJobsWithLease updates each job with lease information.
func (s *serviceImpl) updateJobsWithLease(
	ctx context.Context,
	tx bun.Tx,
	jobs []*models.CheckJob,
	workerUID string,
	now time.Time,
	isPostgres bool,
) error {
	for _, job := range jobs {
		if err := s.updateSingleJobLease(ctx, tx, job, workerUID, now, isPostgres); err != nil {
			return err
		}
	}

	return nil
}

// updateSingleJobLease updates a single job with lease information.
func (s *serviceImpl) updateSingleJobLease(
	ctx context.Context,
	tx bun.Tx,
	job *models.CheckJob,
	workerUID string,
	now time.Time,
	isPostgres bool,
) error {
	// Convert Period to time.Duration
	period := time.Duration(job.Period)

	// Calculate lease expiration: Use scheduled_at + period + 30s
	// This ensures the lease expires at a predictable time regardless of when the job is claimed
	latest := *job.ScheduledAt
	if now.After(latest) {
		latest = now
	}
	leaseExpiresAt := latest.Add(period + 30*time.Second)

	// Update the job
	result, err := tx.NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = ?", workerUID).
		Set("lease_expires_at = ?", leaseExpiresAt).
		Set("lease_starts = lease_starts + 1").
		Set("updated_at = ?", now).
		Where("uid = ?", job.UID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to update check job %s: %w", job.UID, err)
	}

	// For SQLite: Verify the update succeeded (optimistic locking)
	if !isPostgres {
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}

		if rows == 0 {
			// Job was claimed by another runner, return error to trigger retry
			return sql.ErrNoRows
		}
	}

	// Update the job object with new lease info for return
	job.LeaseWorkerUID = &workerUID
	job.LeaseExpiresAt = &leaseExpiresAt
	job.LeaseStarts++
	job.UpdatedAt = now

	return nil
}

// ReleaseLease releases the lease and reschedules the job.
// Resets lease_starts to 0 since the attempt is over.
//
// This variant does not touch cost_ewma_ms; it keeps effective_scheduled_at in
// step with the new schedule by anchoring it to nextScheduledAt (no cost sample
// to apply). Used by the stuck-job reaper, dispatch-time error results, and
// remote backends that have no fresh duration to fold in.
//
// NOT the rate-limit deferral — that path is DeferLeaseRateLimited, which keeps
// the ordering key where it was so the deficit rotates (spec 2026-08-26-02).
func (s *serviceImpl) ReleaseLease(
	ctx context.Context,
	jobUID string,
	workerUID string,
	nextScheduledAt time.Time,
) error {
	update := s.db.NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = NULL").
		Set("lease_expires_at = NULL").
		Set("lease_starts = 0"). // Reset since the attempt is over
		Set("scheduled_at = ?", nextScheduledAt).
		// Re-anchor the ordering key to the new schedule so a released job does
		// not keep an effective deadline from a stale (earlier) schedule. The
		// cost offset is reapplied on the next post-exec write.
		Set("effective_scheduled_at = ?", nextScheduledAt).
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", jobUID).
		Where("lease_worker_uid = ?", workerUID) // Safety: only release if we own the lease

	return s.execRelease(ctx, update)
}

// DeferLeaseRateLimited releases the lease of a rate-limited job WITHOUT
// re-anchoring effective_scheduled_at (spec 2026-08-26-02).
//
// It is ReleaseLease minus one `Set`, and that omission is the entire
// anti-starvation fix. Because the claim orders by effective_scheduled_at ASC,
// a deferred job that keeps its missed tick as the ordering key grows more
// overdue with every window it loses, and therefore sorts ahead of its on-time
// org siblings the next time the org's token bucket has room. Under a
// permanently over-cap org the deficit rotates across all its checks instead of
// landing on the same UID-hash-phase losers forever (the production pathology:
// a 1-minute check with zero results for 7.5 hours while its org ran at rate).
//
// scheduled_at still advances to the next aligned tick, so the claim predicate
// (`scheduled_at <= now + ahead`) keeps a deferred job out of the window it was
// just turned away from — no busy loop, no thundering re-claim.
//
// cost_ewma_ms / delay_ewma_ms / lane are left untouched: no probe ran, so
// there is no sample to fold in, and the delay EWMA is telemetry that must not
// be reset by a deferral.
func (s *serviceImpl) DeferLeaseRateLimited(
	ctx context.Context,
	jobUID string,
	workerUID string,
	nextScheduledAt time.Time,
) error {
	update := s.db.NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = NULL").
		Set("lease_expires_at = NULL").
		Set("lease_starts = 0"). // The probe never started; don't count it as a crash
		Set("scheduled_at = ?", nextScheduledAt).
		// effective_scheduled_at is deliberately NOT set here. See the doc above.
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", jobUID).
		Where("lease_worker_uid = ?", workerUID) // Safety: only release if we own the lease

	return s.execRelease(ctx, update)
}

// ReleaseLeaseWithSchedulingState releases the lease and folds the post-exec
// cost and delay signals — plus the hysteresis-classified lane (spec
// 2026-07-01-03 D2) — into the same UPDATE (spec 2026-06-30-09).
func (s *serviceImpl) ReleaseLeaseWithSchedulingState(
	ctx context.Context,
	jobUID string,
	workerUID string,
	nextScheduledAt time.Time,
	costEWMAMs float64,
	delayEWMAMs float64,
	effectiveScheduledAt time.Time,
	lane uint8,
) error {
	update := s.db.NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = NULL").
		Set("lease_expires_at = NULL").
		Set("lease_starts = 0"). // Reset since job completed
		Set("scheduled_at = ?", nextScheduledAt).
		Set("cost_ewma_ms = ?", costEWMAMs).
		Set("delay_ewma_ms = ?", delayEWMAMs).
		Set("effective_scheduled_at = ?", effectiveScheduledAt).
		Set("lane = ?", lane).
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", jobUID).
		Where("lease_worker_uid = ?", workerUID) // Safety: only release if we own the lease

	return s.execRelease(ctx, update)
}

// execRelease runs a release UPDATE and maps the no-rows case to
// ErrJobClaimedByAnother.
func (s *serviceImpl) execRelease(ctx context.Context, update *bun.UpdateQuery) error {
	result, err := update.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to release lease: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("no rows updated: %w", ErrJobClaimedByAnother)
	}

	return nil
}
