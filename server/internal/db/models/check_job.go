package models

import (
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// CheckJob represents a scheduled job for executing a check.
type CheckJob struct {
	UID             string  `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string  `bun:"organization_uid,notnull"`
	CheckUID        string  `bun:"check_uid"`
	Region          *string `bun:"region"`
	Type            string  `bun:"type"`
	Config          JSONMap `bun:"config,type:jsonb,nullzero"`
	// ConfigPrivate / ConfigPrivateKeys mirror the columns on Check — the
	// scheduler copies them when materializing a job so workers never see
	// plaintext secrets in the row at rest.
	ConfigPrivate     *string `bun:"config_private,type:text,nullzero"`
	ConfigPrivateKeys *string `bun:"config_private_keys,type:text,nullzero"`
	// ConfigSealed mirrors Check.ConfigSealed: the region-sealed (age X25519)
	// envelope shipped VERBATIM to deported agents — the server never decrypts
	// it on the agent dispatch path (spec 2026-07-16-02).
	ConfigSealed   *string            `bun:"config_sealed,type:text,nullzero"`
	Encrypted      bool               `bun:"encrypted,notnull,default:false"`
	Period         timeutils.Duration `bun:"period,notnull"`
	ScheduledAt    *time.Time         `bun:"scheduled_at"`
	LeaseWorkerUID *string            `bun:"lease_worker_uid"`
	LeaseExpiresAt *time.Time         `bun:"lease_expires_at"`
	LeaseStarts    int                `bun:"lease_starts,notnull,default:0"`
	UpdatedAt      time.Time          `bun:"updated_at,notnull,default:current_timestamp"`

	// Cost-aware, plan-weighted scheduling (spec 2026-06-30-09).
	//
	// CostEWMAMs is an exponentially-weighted moving average of execution
	// duration in milliseconds, updated in the post-exec write (ReleaseLease).
	// Timeouts pin it to the ceiling. Drives slow-lane classification and the
	// cost-aware execution timeout. 0 until the job's first run.
	CostEWMAMs float64 `bun:"cost_ewma_ms,notnull,default:0"`
	// DelayEWMAMs is an EWMA of how late the job actually started relative to
	// its real scheduled_at (probe start − scheduled_at, floored at 0), updated
	// in the post-exec write. Pure telemetry (spec 2026-07-01-02): it feeds the
	// cost-distribution endpoint and the lane-split go/no-go but never the claim
	// order — delay is a victim signal, and folding it into the offset punished
	// starved checks and spiraled unboundedly under overload. 0 until the job's
	// first run. Added by migration 007.
	DelayEWMAMs float64 `bun:"delay_ewma_ms,notnull,default:0"`
	// PlanWeight is the denormalized plan tier copied from org_entitlements
	// (0 = free; higher = more protected). Reserved capacity + deadline credit
	// for paid orgs. Refreshed on entitlement change and reconcile.
	PlanWeight int `bun:"plan_weight,notnull,default:0"`
	// EffectiveScheduledAt is scheduled_at + cost_ewma×CostOffsetWeight −
	// tier_credit, with the offset clamped to scheduling.MaxDeprioritizeOffset.
	// The claim SELECT gates on scheduled_at but orders by this column, so
	// de-prioritization only bites under contention (D2/Option A). Backfilled to
	// scheduled_at by migration 006; delay-era offsets healed by migration 008.
	//
	// A rate-limited deferral deliberately does NOT re-anchor this column
	// (checkjobsvc.DeferLeaseRateLimited, spec 2026-08-26-02): a job the per-org
	// bucket turned away keeps the tick it missed as its ordering key, so it
	// grows more overdue every window it loses and wins the next contended slot.
	// That is what makes an over-cap org rotate its deficit instead of starving
	// the same UID-hash phases forever.
	EffectiveScheduledAt *time.Time `bun:"effective_scheduled_at"`
	// Lane is the scheduling class (spec 2026-07-01-03): 0 = fast, 1 = slow
	// (scheduling.LaneFast / LaneSlow). Classified from the cost EWMA with
	// hysteresis in the post-exec write; new rows start fast (first run is
	// FIFO; one execution reclassifies). The claim runs two lane-filtered
	// SELECTs so slow jobs can never occupy more than pool_size −
	// fast_lane_reserved slots on a worker. Added by migration 009.
	Lane uint8 `bun:"lane,notnull,default:0"`

	// Check is the check this job executes, populated at claim time by
	// ClaimJobs / ClaimJobsForCheck so the incident hot path can skip a
	// per-result GetCheck round-trip. Transient: never persisted (bun:"-").
	// May be nil if the check was deleted between scheduling and claim.
	Check *Check `bun:"-"`
}

// NewCheckJob creates a new check job with generated UID.
func NewCheckJob(orgUID string, checkUID string, period timeutils.Duration) *CheckJob {
	now := time.Now()

	return &CheckJob{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		CheckUID:        checkUID,
		Period:          period,
		Config:          make(JSONMap),
		ScheduledAt:     &now,
		// Anchor the WFQ ordering key to scheduled_at on creation; the
		// post-exec write refines it once a cost signal exists.
		EffectiveScheduledAt: &now,
		// New jobs start in the fast lane (Lane zero value = 0): the first run
		// is FIFO and the post-exec write reclassifies from the measured cost.
		Lane:      0,
		UpdatedAt: now,
	}
}

// IsInternal reports whether this job belongs to an internal, server-created
// check (worker self-stats plumbing).
//
// ONE HELPER SO THE TWO RATE GATES CANNOT DISAGREE — the in-process worker gate
// (checkworker.applyRateLimitGate) and the agent dispatch gate
// (agentws.handleClaim) both call it, the same way both delegate "what is a
// passive check" to checkerdef. An internal check is exempt from the MaxChecks
// quota and invisible to the checks-per-minute demand figure, so it must not
// draw MaxChecksPerMinute tokens either (spec 2026-08-27-01): counting nowhere
// has to mean counting nowhere, or the predicted demand and the factual skip
// counter describe different fleets.
//
// Reads the check attached at claim time (checkjobsvc.attachChecks, inside the
// claim transaction) — no column on check_jobs, no migration. A nil Check means
// the row was deleted between scheduling and claim; that job is treated as a
// normal metered one, which is the safe direction.
func (j *CheckJob) IsInternal() bool {
	return j.Check != nil && j.Check.Internal
}

// CheckJobUpdate represents fields that can be updated.
type CheckJobUpdate struct {
	Region            *string
	Config            *JSONMap
	ConfigPrivate     *string
	ConfigPrivateKeys *string
	Encrypted         *bool
	Period            *timeutils.Duration
	ScheduledAt       *time.Time
	LeaseWorkerUID    *string
	LeaseExpiresAt    *time.Time
	LeaseStarts       *int
}
