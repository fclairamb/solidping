package models

import (
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// CheckStatus represents the health status of a check.
type CheckStatus int

const (
	// CheckStatusCreated indicates the check was just created and hasn't been executed yet.
	CheckStatusCreated CheckStatus = 1
	// CheckStatusUp indicates the check is healthy.
	CheckStatusUp CheckStatus = 3
	// CheckStatusDown indicates the check is failing.
	CheckStatusDown CheckStatus = 4
	// CheckStatusValidating is the transient state between "first failure
	// observed" and "incident opens" — the failure has been seen but the
	// configured ConfirmationPeriod hasn't elapsed yet. It never triggers
	// notifications on its own, but it DOES gate the incident state machine
	// of its dependents: a hard child whose confirmation elapses while an
	// ancestor is still validating is held (spec 2026-08-31-06,
	// incidents.ancestorHoldRemaining).
	CheckStatusValidating CheckStatus = 5
	// CheckStatusDegraded is the aggregated/summary status: a rolled-up window
	// contained warning(s) but no dominating failure. Not produced by the live
	// pipeline (which uses CheckStatusWarning); retained for rendering a
	// check's aggregated/summary status and as a valid ?status= filter value.
	CheckStatusDegraded CheckStatus = 7
	// CheckStatusWarning is the live current status: the target is up but
	// there is something to report. Display-only like CheckStatusValidating —
	// never triggers notifications, never gates the incident state machine.
	CheckStatusWarning CheckStatus = 8
	// CheckStatusStale means "no data": the check's newest real result, across
	// every region, is older than StaleThreshold(period) (spec 2026-09-25-02).
	// It is neither up nor down. Only the freshness sweeper enters it (a
	// guarded compare-and-set that bypasses the incident pipeline entirely);
	// the next real result leaves it through ProcessCheckResult.
	//
	// 10, not the free 2/6/9: check and result statuses share one integer
	// space by convention (1 created, 3 up, 4 down, 7 degraded, 8 warning mean
	// the same on both columns) and 1-9 are all live result codes — 9 is
	// ResultStatusAbandoned. 10 can never be misread as a result.
	CheckStatusStale CheckStatus = 10
)

// staleMinThreshold is the floor of the staleness threshold: a 10-second
// check is not declared dead after 30 seconds of silence.
const staleMinThreshold = 5 * time.Minute

// stalePeriodMultiplier is how many periods of silence make a check stale.
const stalePeriodMultiplier = 3

// StaleThreshold is how long a check may go without a real result before it
// is stale: max(3 × period, 5 min). One definition, read by the sweeper, the
// badge and the API, so they can never disagree on what "no data" means.
func StaleThreshold(period time.Duration) time.Duration {
	threshold := stalePeriodMultiplier * period
	if threshold < staleMinThreshold {
		return staleMinThreshold
	}

	return threshold
}

// IsPassive reports whether the check is passive (heartbeat, email): driven by
// an inbound signal, evaluated on the jobs node, never inside a region (spec
// 2026-09-25-04).
func (c *Check) IsPassive() bool {
	return checkerdef.CheckType(c.Type).IsPassive()
}

// JobRegions is the region set the check's jobs are materialized for. A
// passive check has none, whatever its row says: it makes no outbound request,
// so a region adds nothing but a place for its evaluator to die (spec
// 2026-09-25-04). Every materialization point (createCheckJobs on both
// engines, reconcileCheckJobs) reads this rather than Regions.
func (c *Check) JobRegions() []string {
	if c.IsPassive() {
		return nil
	}

	return c.Regions
}

// NormalizePassiveRegions empties Regions on a passive check. An explicit list
// is accepted and dropped rather than rejected, so an existing config-as-code
// file that names a region on a heartbeat keeps applying (spec 2026-09-25-04).
func (c *Check) NormalizePassiveRegions() {
	if c.IsPassive() {
		c.Regions = []string{}
	}
}

// StaleThreshold is the check's own staleness threshold.
func (c *Check) StaleThreshold() time.Duration {
	return StaleThreshold(time.Duration(c.Period))
}

// FreshnessReference is the instant the staleness rule measures from: the
// newest real result, or the creation time for a check that never produced
// one (it should have run by then).
func (c *Check) FreshnessReference() time.Time {
	if c.LastResultAt != nil {
		return *c.LastResultAt
	}

	return c.CreatedAt
}

// IsDataStale reports whether the check's newest real result is older than
// its threshold as of now — the raw freshness fact, independent of whether
// the sweeper has already written CheckStatusStale.
func (c *Check) IsDataStale(now time.Time) bool {
	return now.Sub(c.FreshnessReference()) > c.StaleThreshold()
}

// String returns the lowercase wire name for a CheckStatus, used by the
// dashboard to key status colors and labels. Unknown values fall back to
// "unknown" so an unset DB column never blows up the UI.
func (s CheckStatus) String() string {
	switch s {
	case CheckStatusCreated:
		return WireStatusCreated
	case CheckStatusUp:
		return WireStatusUp
	case CheckStatusDown:
		return WireStatusDown
	case CheckStatusValidating:
		return WireStatusValidating
	case CheckStatusDegraded:
		return WireStatusDegraded
	case CheckStatusWarning:
		return WireStatusWarning
	case CheckStatusStale:
		return WireStatusStale
	default:
		return WireStatusUnknown
	}
}

// CheckStatusCount is one row of the org-wide check aggregation
// (spec 2026-08-02-06): the number of checks sharing a (status, enabled)
// pair. Produced by db.Service.GetCheckStatusCounts on both dialects and
// folded into the checks stats response.
type CheckStatusCount struct {
	Status  CheckStatus `bun:"status"`
	Enabled bool        `bun:"enabled"`
	Count   int         `bun:"count"`
}

// Check represents a monitoring configuration.
type Check struct {
	UID             string  `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string  `bun:"organization_uid,notnull"`
	CheckGroupUID   *string `bun:"check_group_uid"`
	Name            *string `bun:"name"`
	Slug            *string `bun:"slug"`
	Description     *string `bun:"description"`
	Type            string  `bun:"type,notnull"`
	Config          JSONMap `bun:"config,type:jsonb,nullzero"`
	// ConfigPrivate holds the AES-GCM envelope (JSON) for the secret keys
	// split out of Config at write time. NULL when no encrypted secrets exist
	// on this row — distinct from "encryption disabled at the server".
	ConfigPrivate *string `bun:"config_private,type:text,nullzero"`
	// ConfigPrivateKeys is a JSON array of the key names (e.g. `["password"]`)
	// whose values live in ConfigPrivate. Non-secret by construction; surfaced
	// to the dashboard so it can render placeholder hints without decrypting.
	ConfigPrivateKeys *string `bun:"config_private_keys,type:text,nullzero"`
	// ConfigSealed holds the region-sealed (age X25519, v2) envelope of the same
	// secret keys when the check targets one or more org-private regions (spec
	// 2026-07-16-02): sealed to the X25519 keys of the region's active agents.
	// A check targeting ONLY private regions stores secrets sealed-only
	// (ConfigPrivate stays NULL — the server cannot decrypt them after write);
	// a mixed private+cloud check dual-stores (v1 envelope for cloud dispatch +
	// this sealed blob for agents).
	ConfigSealed *string            `bun:"config_sealed,type:text,nullzero"`
	Regions      []string           `bun:"regions,type:text[],array"`
	Enabled      bool               `bun:"enabled,notnull"`
	Internal     bool               `bun:"internal,notnull"`
	Period       timeutils.Duration `bun:"period,notnull"`

	// CreatedBy is the users.uid of whoever created this check, or NULL when
	// nobody did — the startup job's seeded samples, and every check that
	// predates the column (spec 2026-09-06-02). It is recorded for EVERY
	// creator, not only demo sessions: "who made this" is useful audit data in
	// its own right, and a column populated on one code path only is a column
	// nobody can trust.
	//
	// Deliberately not a foreign key: a check outlives the account that made
	// it, and users are soft-deleted. This is a historical attribution.
	//
	// It is also what makes seeded demo checks immutable to a demo session
	// without any "protected" flag: the ownership rule is
	// `created_by == claims.UserUID`, and NULL never equals a UID.
	CreatedBy *string `bun:"created_by,nullzero"`

	// RegionSpread is the optional inter-region scheduling offset ("spread")
	// applied between consecutive regions' phases (spec 2026-07-20-05). NULL =
	// the default of Period / region_count (even coverage across the period);
	// a non-null value forces a fixed offset (e.g. 0 = all regions fire
	// together for comparative cross-region sampling), validated
	// 0 <= RegionSpread < Period. It is a first-class scheduling input (it
	// drives check_jobs phase), not checker config, hence a column like Period.
	RegionSpread *timeutils.Duration `bun:"region_spread,nullzero"`

	// Incident tracking — wall-clock periods (seconds). Replaces the old
	// count-based thresholds per spec
	// 2026-05-08-02-time-based-confirmation-and-recovery-periods.md.
	// `0` means "open / resolve immediately on the first opposite signal".
	ConfirmationPeriodSeconds int `bun:"confirmation_period_seconds,notnull"`
	RecoveryPeriodSeconds     int `bun:"recovery_period_seconds,notnull"`
	// EscalationThreshold remains streak-based for now — it gates the *second*
	// notification step, not the incident open. Will be re-modeled when the
	// escalation-severity primitive ships. No `default:` clause even though
	// the column has one — see the StatusPage.AutoPublishDelaySeconds note:
	// `default:3` made `escalation_threshold: 0` unwritable on create.
	EscalationThreshold int `bun:"escalation_threshold,notnull"`
	// FirstFailureAt is set on the result that flips the streak from 0 to 1
	// on a failing check (no active incident yet). Cleared on the next success.
	// The incident opens when now - FirstFailureAt >= ConfirmationPeriod.
	FirstFailureAt *time.Time `bun:"first_failure_at"`
	// FirstSuccessSinceFailureAt is set on the first success arriving while
	// an incident is open. Cleared by any subsequent failure during the
	// recovery window. Auto-resolve fires when
	// now - FirstSuccessSinceFailureAt >= RecoveryPeriod.
	FirstSuccessSinceFailureAt *time.Time `bun:"first_success_since_failure_at"`

	// Adaptive resolution settings.
	//
	// ReopenCooldownMultiplier (nil = code default) drives the short
	// blip-dedup window: a fast relapse reattaches to the just-resolved
	// incident instead of paging again. Independent of the flapping layer.
	ReopenCooldownMultiplier *int `bun:"reopen_cooldown_multiplier"`

	// Flapping (adaptive recovery) config — spec 2026-06-30-07. When a check
	// flaps (repeated outages over a short horizon) the required stability
	// before auto-resolving grows per flap, bounded by a cap. Off-by-default-
	// equivalent: FlapBackoffFactor==1 or FlappingWindowSeconds==0 reproduces
	// the constant RecoveryPeriodSeconds behavior.
	//
	// Which is precisely why none of the three carries a `default:` clause,
	// even though all three columns have one — see the
	// StatusPage.AutoPublishDelaySeconds note. With `default:21600` on the tag,
	// `flappingWindowSeconds: 0` never reached the database, so flapping could
	// not be turned off at creation time (spec 2026-08-30-04). NewCheck
	// supplies the 21600/2/8 defaults instead.
	FlappingWindowSeconds int `bun:"flapping_window_seconds,notnull"`
	FlapBackoffFactor     int `bun:"flap_backoff_factor,notnull"`
	MaxRecoveryMultiplier int `bun:"max_recovery_multiplier,notnull"`

	// Degraded detection — spec 2026-09-22-03. One rule primitive, "M of the
	// last N countable probes match", applied to two populations on this check:
	// failures (status not in up/warning) and slow successes (up/warning with
	// duration above SlowThresholdMs). Evaluated by a periodic sweep, never by
	// the worker; the result status is not touched.
	//
	// M = 0 disables a rule. SlowThresholdMs = 0 disables the slow rule
	// outright (there is no threshold a duration could exceed).
	//
	// ALL FIVE ARE POINTERS, AND nil IS THE INTERESTING ONE: nil means "not
	// configured", the column is NULL, and the code default
	// (DefaultDegradedFailures & co) applies — read through the
	// EffectiveDegraded* accessors below, never off the raw field.
	//
	// The three-way distinction is what the whole shape exists for:
	//
	//	nil   not configured -> the documented default (5 / 60, 3 / 6, 0)
	//	&0    explicitly OFF (the documented way to disable a rule)
	//	&7    explicitly 7
	//
	// A plain int could not tell "unset" from "off", which is why the columns
	// used to be `not null default 5` while the struct carried no `default:`
	// bun tag (with `default:5` on the tag, `degraded_failures: 0` never
	// reaches the database and the rule cannot be turned off at creation time
	// — spec 2026-08-30-04, and before it StatusPage.AutoPublishDelaySeconds).
	// That combination worked but pushed the defaulting into Go at WRITE time:
	// NewCheck hardcoded 5/60/3/6/0 and any other insert path silently wrote 0
	// for all five, i.e. five rules quietly off. Resolving at READ time instead
	// means a caller that does not mention these fields gets the defaults for
	// free. Do not reintroduce a `default:` tag or a SQL default clause.
	DegradedFailures       *int `bun:"degraded_failures"`
	DegradedFailuresWindow *int `bun:"degraded_failures_window"`
	DegradedSlow           *int `bun:"degraded_slow"`
	DegradedSlowWindow     *int `bun:"degraded_slow_window"`
	SlowThresholdMs        *int `bun:"slow_threshold_ms"`
	// DegradedEnabled gates OPENING incidents, not evaluating. FALSE on every
	// pre-existing row (the migration's column default) and TRUE on every check
	// created from now on (NewCheck): upgrading must never start paging on its
	// own, per the rule already written at SLOAlertPolicy's rollout.
	//
	// It is deliberately NOT a pointer, unlike the five above: NULL cannot
	// carry that rollout rule. nil-means-true would start paging on upgrade,
	// nil-means-false would silently disable checks created by a path that does
	// not set the flag. A plain bool defaulting to false makes every such path
	// fail SAFE — into the dry run, which stamps DegradedWouldFireAt and pages
	// nobody.
	DegradedEnabled bool `bun:"degraded_enabled,notnull"`
	// DegradedWouldFireAt is the dry run's output: when the evaluator last saw
	// a degraded condition on a check that has DegradedEnabled false. It is
	// what the check page's "this check would have been flagged degraded at
	// 14:37 — enable?" banner and the checks list's `wouldHaveFired` filter
	// read. Cleared once the check is enabled, so the two states can never both
	// look true.
	DegradedWouldFireAt *time.Time `bun:"degraded_would_fire_at"`
	// DegradedEvaluatedAt is evaluator rotation STATE, not configuration: the
	// sweep reads checks oldest-evaluated first so a bounded per-sweep batch
	// still gives every check a turn on a large install, exactly as
	// slo_alert_policies.last_evaluated_at does for burn rates.
	DegradedEvaluatedAt *time.Time `bun:"degraded_evaluated_at"`

	// Flap state, updated only on the rare incident-open/reopen (never per
	// result). FlapCount is the number of outages accumulated inside the
	// rolling flapping window; LastOutageAt is the wall-clock of the most
	// recent outage onset and gates the window reset.
	FlapCount    int        `bun:"flap_count,notnull"`
	LastOutageAt *time.Time `bun:"last_outage_at"`

	// Optional escalation policy. Falls back to the check_group's policy
	// (and ultimately to no escalation) when nil.
	EscalationPolicyUID *string `bun:"escalation_policy_uid"`

	// TracerouteOnFailure is the per-check override for the MTR-style path
	// capture taken when this check goes down on a network-reachability
	// failure (spec 2026-08-21-10).
	//
	// THREE STATES, AND nil IS THE INTERESTING ONE:
	//
	//	nil     inherit the org default (org parameter
	//	        `diagnostics.traceroute.enabled`, itself ON per the spec)
	//	&true   always trace this check
	//	&false  never trace this check, whatever the org default says
	//
	// A plain bool would collapse "not decided" into "no", which would make
	// the org-level default unreachable for every check that already exists.
	TracerouteOnFailure *bool `bun:"traceroute_on_failure"`

	// Status tracking
	Status          CheckStatus `bun:"status,notnull"`
	StatusStreak    int         `bun:"status_streak,notnull"`
	StatusChangedAt *time.Time  `bun:"status_changed_at"`
	// LastResultAt is the execution time of the newest REAL result (up, down,
	// timeout, error, warning — never the created/running/abandoned
	// placeholders), across every region. Denormalized so the freshness sweep
	// is one indexed query instead of a scan of `results` (spec 2026-09-25-02).
	// Written only by incidents.ProcessCheckResult, including for checks in
	// maintenance. NULL for a check that never produced a result.
	LastResultAt *time.Time `bun:"last_result_at"`

	CreatedAt time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt *time.Time `bun:"deleted_at"`

	// GroupSortKey is the effective group-ordering key populated only by the
	// sort=group ListChecks path: the check's group sort_order, or a large
	// sentinel (int16 max is 32767, so ungrouped sorts strictly last). Scan-only
	// and transient — never selected, inserted, or updated outside that query.
	GroupSortKey int64 `bun:"group_sort_key,scanonly"`

	// TargetHostSortKey is the effective sort=targetHost ordering key: the
	// check's config host/url/target text (best-effort, not hostname-parsed —
	// see targetHostSortKeyExpr), or a sentinel that sorts strictly last for
	// checks with none of those fields. Scan-only and transient; distinct from
	// the response's TargetHost (checkerdef.ExtractTargetHost), which is the
	// precise, hostname-parsed value clients bucket by.
	TargetHostSortKey string `bun:"target_host_sort_key,scanonly"`
}

// CheckConfigKeyTimeout is the check-config key holding the optional
// per-check execution timeout, stored as a Go duration string
// (spec 2026-07-11-05).
const CheckConfigKeyTimeout = "timeout"

// TimeoutOrDefault resolves the check's per-execution timeout: the explicit
// `timeout` entry in its config when it parses to a positive duration, and
// `defaultTimeout` (the server's scheduling.check_timeout_ms) otherwise.
//
// This is a READ-side approximation of what the worker actually applies —
// the worker additionally clamps an unset timeout by the cost EWMA and caps
// an explicit one (checkworker.perCheckTimeout). Both consumers here want an
// upper bound on "how late can this check possibly notice an outage", and the
// unclamped default is exactly that bound: the cost-aware clamp only ever
// shortens it.
func (c *Check) TimeoutOrDefault(defaultTimeout time.Duration) time.Duration {
	raw, ok := c.Config[CheckConfigKeyTimeout].(string)
	if !ok || raw == "" {
		return defaultTimeout
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return defaultTimeout
	}

	return parsed
}

// RegionSpreadDuration returns the check's optional inter-region spread
// override as a *time.Duration (nil when unset), for the
// scheduling.RegionSpread resolver. Keeps the *timeutils.Duration ⇄
// *time.Duration conversion in one place so the reconcile, create, and worker
// paths all resolve the identical spread.
func (c *Check) RegionSpreadDuration() *time.Duration {
	if c.RegionSpread == nil {
		return nil
	}

	d := time.Duration(*c.RegionSpread)

	return &d
}

// recoveryHardCeiling bounds the effective recovery period in wall-clock
// time, mirroring the reopen-cooldown clamp (see calculateCooldown in the
// incidents package). Even a long backoff or a large multiplier can never
// push the required stability beyond this.
const recoveryHardCeiling = 30 * time.Minute

// FlappingWindowElapsed reports whether the rolling flapping window has
// elapsed as of `now`, i.e. whether the NEXT outage onset would start a fresh
// window rather than count as a flap inside the current one. True when there
// has been no outage yet (LastOutageAt nil), the flapping feature is off
// (FlappingWindowSeconds == 0), or the last outage is older than the window.
//
// This is the one place the "lazy reset" rule is expressed — both the
// write-path counter bump (incidents.bumpFlap, via this method) and the
// read-path effective-value exposure (EffectiveFlapCount) delegate to it, so
// the two can never drift apart.
func (c *Check) FlappingWindowElapsed(now time.Time) bool {
	if c.LastOutageAt == nil {
		return true
	}

	window := time.Duration(c.FlappingWindowSeconds) * time.Second
	if window == 0 {
		return true
	}

	return now.Sub(*c.LastOutageAt) > window
}

// EffectiveFlapCount returns the number of outages counted inside the
// current rolling flapping window, as of `now`.
//
// THE LAZY-RESET TRAP: FlapCount (the raw column) only resets to 0 at the
// NEXT outage onset (see incidents.bumpFlap) — a check whose last outage was
// e.g. 12h ago can still hold a stale nonzero FlapCount in the row, because
// nothing has come along yet to reset it. Any caller that reads FlapCount
// directly to describe the check's CURRENT state (rather than to drive the
// active incident's own recovery math, where it is always fresh) must use
// this method instead, or it will report a flap level that stopped being
// true hours or days ago.
func (c *Check) EffectiveFlapCount(now time.Time) int {
	if c.FlappingWindowElapsed(now) {
		return 0
	}

	return c.FlapCount
}

// effectiveRecoveryPeriodForFlapCount is the shared math behind
// EffectiveRecoveryPeriod and EffectiveRecoveryPeriodAt:
//
//	effective = min( R · F^flapCount , R · MaxRecoveryMultiplier , HARD_CEILING )
//
// where R = RecoveryPeriodSeconds and F = FlapBackoffFactor. It short-circuits
// to a plain R (today's constant behavior) when the flapping feature is off
// for this check — F<=1, FlappingWindowSeconds==0, or flapCount<=0 — so
// existing checks never regress.
func (c *Check) effectiveRecoveryPeriodForFlapCount(flapCount int) time.Duration {
	base := time.Duration(c.RecoveryPeriodSeconds) * time.Second

	if c.FlapBackoffFactor <= 1 || c.FlappingWindowSeconds == 0 || flapCount <= 0 {
		return base
	}

	// Cap multiplier: required recovery never exceeds R × MaxRecoveryMultiplier.
	capMult := c.MaxRecoveryMultiplier
	if capMult < 1 {
		capMult = 1
	}

	// Compute F^flapCount in integer space, short-circuiting once it reaches or
	// exceeds the cap so a large flapCount can't overflow.
	multiplier := 1
	for range flapCount {
		multiplier *= c.FlapBackoffFactor
		if multiplier >= capMult {
			multiplier = capMult

			break
		}
	}

	effective := base * time.Duration(multiplier)
	if effective > recoveryHardCeiling {
		effective = recoveryHardCeiling
	}

	return effective
}

// EffectiveRecoveryPeriod returns the stability required before an incident
// on this check auto-resolves, given the check's RAW (possibly stale)
// FlapCount. This is what the incidents package uses while an incident is
// active: FlapCount was just written by bumpFlap at this incident's own
// onset, so it is always fresh in that context — the lazy-reset trap does not
// apply here. See effectiveRecoveryPeriodForFlapCount for the math.
func (c *Check) EffectiveRecoveryPeriod() time.Duration {
	return c.effectiveRecoveryPeriodForFlapCount(c.FlapCount)
}

// EffectiveRecoveryPeriodAt returns the same computation as
// EffectiveRecoveryPeriod, but driven by EffectiveFlapCount(now) rather than
// the raw column — i.e. it is lazy-reset aware. Use this to describe a
// check's CURRENT adaptive-recovery state from outside an active incident
// (e.g. the API's flapState block), where the raw FlapCount may be stale.
func (c *Check) EffectiveRecoveryPeriodAt(now time.Time) time.Duration {
	return c.effectiveRecoveryPeriodForFlapCount(c.EffectiveFlapCount(now))
}

// The fleet-calibrated degraded-detection defaults (spec 2026-09-22-03), which
// a NULL column resolves to at read time.
//
// 5-of-60 is the only failure rule that catches the motivating episode. The
// slow rule ships INERT — DefaultSlowThresholdMs is 0, so no duration can
// exceed it — because there is no honest fleet-wide value for "too slow" and
// auto-baselining one is an explicit non-goal. 3-of-6 is therefore the shape
// the rule takes once an operator commits to a threshold, not a rule that runs
// on its own.
const (
	DefaultDegradedFailures       = 5
	DefaultDegradedFailuresWindow = 60
	DefaultDegradedSlow           = 3
	DefaultDegradedSlowWindow     = 6
	DefaultSlowThresholdMs        = 0
)

// EffectiveDegradedFailures resolves M for the failure rule: the stored value
// when the operator configured one (including an explicit 0, which turns the
// rule off), the code default when the column is NULL.
//
// Every reader of the degraded configuration goes through these five accessors.
// Dereferencing the raw pointer would panic on an unconfigured check, and
// treating nil as 0 would silently disable the rules — the precise failure this
// feature exists to eliminate.
func (c *Check) EffectiveDegradedFailures() int {
	return intOrDefault(c.DegradedFailures, DefaultDegradedFailures)
}

// EffectiveDegradedFailuresWindow resolves N for the failure rule.
func (c *Check) EffectiveDegradedFailuresWindow() int {
	return intOrDefault(c.DegradedFailuresWindow, DefaultDegradedFailuresWindow)
}

// EffectiveDegradedSlow resolves M for the slow rule.
func (c *Check) EffectiveDegradedSlow() int {
	return intOrDefault(c.DegradedSlow, DefaultDegradedSlow)
}

// EffectiveDegradedSlowWindow resolves N for the slow rule.
func (c *Check) EffectiveDegradedSlowWindow() int {
	return intOrDefault(c.DegradedSlowWindow, DefaultDegradedSlowWindow)
}

// EffectiveSlowThresholdMs resolves the duration above which a successful probe
// counts as slow. 0 (stored or defaulted) means the slow rule is off.
func (c *Check) EffectiveSlowThresholdMs() int {
	return intOrDefault(c.SlowThresholdMs, DefaultSlowThresholdMs)
}

// intOrDefault returns *value when value is set, fallback otherwise.
func intOrDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}

	return *value
}

// NewCheck creates a new check with generated UID.
//
// Period is deliberately a FLAT one-minute constant here, not a type-aware
// default: this constructor has no access to checkerdef metadata (it lives in
// the models package, below checkerdef in the import graph), and its two
// callers that matter for a stored period both overwrite it anyway —
// checks.Service.CreateCheck resolves the real value through
// defaultPeriodForType (server/internal/handlers/checks/validate.go) right
// after constructing the check, and CloneCheck's cloneBuildCheck always
// copies Period from the source row. Every other direct caller (checkworker's
// and jobworker's self-stats checks) marks the check Internal, which
// validatePeriodForType exempts from the floor entirely. So: if a check ever
// again shows up storing a period below its own type's MinPeriod, the bug is
// in one of those resolvers, not in this flat constant — do not "fix" it here
// (spec 2026-09-11-07).
func NewCheck(orgUID, slug, checkType string) *Check {
	now := time.Now()

	var slugPtr *string
	if slug != "" {
		slugPtr = &slug
	}

	return &Check{
		UID:                       uuid.New().String(),
		OrganizationUID:           orgUID,
		Slug:                      slugPtr,
		Type:                      checkType,
		Config:                    make(JSONMap),
		Enabled:                   true,
		Period:                    timeutils.Duration(time.Minute), // default to 1 minute
		ConfirmationPeriodSeconds: 120,
		EscalationThreshold:       10,
		RecoveryPeriodSeconds:     120,
		FlappingWindowSeconds:     21600, // 6h
		FlapBackoffFactor:         2,
		MaxRecoveryMultiplier:     8,
		// Degraded detection: the five numeric fields are deliberately left nil
		// — NULL in the database, resolved to DefaultDegradedFailures & co by
		// the EffectiveDegraded* accessors at read time. Assigning 5/60/3/6/0
		// here would be the bug this shape replaced: it puts the defaults in
		// ONE constructor, so every other insert path has to remember to repeat
		// them or silently store five disabled rules. The assignments are
		// absent on purpose — do not "fix" it by adding them back.
		//
		// degraded_enabled is the exception and must be written: ON for a new
		// check, OFF for every pre-existing row (the migration's column
		// default). See the DegradedEnabled field comment.
		DegradedEnabled: true,
		Status:          CheckStatusCreated,
		StatusStreak:    0,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// CheckRate is a thin projection of a check used to compute usage stats:
// whether the check is enabled, its execution period, and its region set.
// Returned by ListOrgCheckRates so the entitlements service can sum the
// aggregate checks-per-minute in Go (the SQL interval/text representation of
// Period is not portable for a SUM(60/period) across Postgres and SQLite).
// Regions is needed because a multi-region check executes once per region per
// period, so its per-minute cost is (60s/period) × max(1, len(Regions)).
// Type is needed to exclude passive types (heartbeat, email) from the demand
// measured against MaxChecksPerMinute: they return before the token gate and
// consume no execution budget (spec 2026-08-26-03).
type CheckRate struct {
	// UID identifies the row so a caller can project a hypothetical change:
	// drop the check being edited out of the sum and add its proposed shape
	// back (spec 2026-08-26-05's validate-time rate warning).
	UID     string             `bun:"uid"`
	Enabled bool               `bun:"enabled"`
	Period  timeutils.Duration `bun:"period"`
	Regions []string           `bun:"regions,type:text[],array"`
	Type    string             `bun:"type"`
}

// CheckUpdate represents fields that can be updated.
type CheckUpdate struct {
	CheckGroupUID      *string
	Name               *string
	Slug               *string
	Description        *string
	Type               *string
	Config             *JSONMap
	ConfigPrivate      *string
	ConfigPrivateKeys  *string
	ClearConfigPrivate bool
	ConfigSealed       *string
	ClearConfigSealed  bool
	Regions            *[]string
	Enabled            *bool
	Internal           *bool
	Period             *timeutils.Duration
	// RegionSpread sets the inter-region offset override; ClearRegionSpread
	// resets it to NULL (revert to the period/region_count default).
	RegionSpread      *timeutils.Duration
	ClearRegionSpread bool

	// Incident tracking — wall-clock periods replacing the legacy count
	// thresholds. EscalationThreshold stays count-based for now.
	ConfirmationPeriodSeconds *int
	RecoveryPeriodSeconds     *int
	EscalationThreshold       *int

	// FirstFailureAt / FirstSuccessSinceFailureAt drive the open/resolve
	// clocks; ProcessCheckResult sets/clears them as the streak signal flips.
	FirstFailureAt                  *time.Time
	FirstSuccessSinceFailureAt      *time.Time
	ClearFirstFailureAt             bool
	ClearFirstSuccessSinceFailureAt bool

	// Adaptive resolution settings
	ReopenCooldownMultiplier *int

	// Flapping (adaptive recovery) config — spec 2026-06-30-07.
	FlappingWindowSeconds *int
	FlapBackoffFactor     *int
	MaxRecoveryMultiplier *int

	// Degraded detection config — spec 2026-09-22-03.
	DegradedFailures       *int
	DegradedFailuresWindow *int
	DegradedSlow           *int
	DegradedSlowWindow     *int
	SlowThresholdMs        *int
	DegradedEnabled        *bool
	// DegradedWouldFireAt / DegradedEvaluatedAt are written by the evaluator
	// sweep, never by an API caller. Clear* sets the column to NULL.
	DegradedWouldFireAt      *time.Time
	ClearDegradedWouldFireAt bool
	DegradedEvaluatedAt      *time.Time

	// Optional escalation policy override (nil = inherit from group / none)
	EscalationPolicyUID *string

	// TracerouteOnFailure sets the per-check path-trace override;
	// ClearTracerouteOnFailure resets it to NULL (inherit the org default).
	TracerouteOnFailure      *bool
	ClearTracerouteOnFailure bool

	// Clear* fields set the corresponding column to NULL on update.
	ClearEscalationPolicyUID bool

	// Status tracking (internal use)
	Status          *CheckStatus
	StatusStreak    *int
	StatusChangedAt *time.Time
}

// Label represents a key-value pair for categorizing checks.
type Label struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	Key             string     `bun:"key,notnull"`
	Value           string     `bun:"value,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewLabel creates a new label with generated UID.
func NewLabel(orgUID, key, value string) *Label {
	now := time.Now()

	return &Label{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Key:             key,
		Value:           value,
		CreatedAt:       now,
	}
}

// LabelSuggestion is one row of an autocomplete query: either a label key
// (when listing distinct keys) or a label value (when listing distinct values
// for a given key), together with the number of distinct checks carrying it.
type LabelSuggestion struct {
	Value string
	Count int
}

// CheckLabel represents the many-to-many relationship between checks and labels.
type CheckLabel struct {
	UID       string    `bun:"uid,pk,type:varchar(36)"`
	CheckUID  string    `bun:"check_uid,notnull"`
	LabelUID  string    `bun:"label_uid,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
}

// NewCheckLabel creates a new check-label relationship with generated UID.
func NewCheckLabel(checkUID, labelUID string) *CheckLabel {
	return &CheckLabel{
		UID:       uuid.New().String(),
		CheckUID:  checkUID,
		LabelUID:  labelUID,
		CreatedAt: time.Now(),
	}
}

// ListChecksFilter provides filtering options for listing checks.
type ListChecksFilter struct {
	Labels        map[string]string // key:value pairs for AND filtering
	CheckGroupUID *string           // filter by check group UID; "none" = ungrouped checks only
	Query         string            // search term for name/slug (case-insensitive substring)
	Types         []string          // optional filter by check type (e.g. ["ssh"]); empty = every type
	Internal      *string           // "true", "false", or "all" — filter by internal status
	Statuses      []CheckStatus     // optional filter by current status (up/down/etc.)
	// WouldHaveFired restricts to checks the degraded dry run has flagged:
	// `degraded_would_fire_at IS NOT NULL` (spec 2026-09-22-03). It is how an
	// operator finds what enabling degraded detection would have caught, and it
	// is the whole adoption path for a feature that ships off.
	WouldHaveFired  bool
	Limit           int        // max results to return (0 = no limit)
	CursorCreatedAt *time.Time // cursor: created_at of last item from previous page
	CursorUID       *string    // cursor: uid of last item from previous page

	// SortByGroup opts into display-order pagination (sort=group): group
	// sort_order asc, ungrouped last, then created_at DESC / uid DESC within a
	// bucket. Off = the default created_at DESC / uid DESC ordering.
	SortByGroup bool
	// CursorGroupSortKey is the effective group sort key of the last item from
	// the previous page — the leading component of the composite sort=group
	// cursor. Only set alongside CursorCreatedAt/CursorUID when SortByGroup.
	CursorGroupSortKey *int64

	// SortByTargetHost opts into the by-host-view pagination (sort=targetHost):
	// targetHost sort key ascending (checks with none of host/url/target last),
	// then name ascending, then uid ascending as the final tiebreaker.
	SortByTargetHost bool
	// CursorTargetHostKey and CursorTargetHostName are the leading two
	// components of the composite sort=targetHost cursor (the third, uid, reuses
	// CursorUID). Only set alongside CursorUID when SortByTargetHost.
	CursorTargetHostKey  *string
	CursorTargetHostName *string
}
