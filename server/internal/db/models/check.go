package models

import (
	"time"

	"github.com/google/uuid"

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
	// configured ConfirmationPeriod hasn't elapsed yet. Display-only:
	// never triggers notifications, never gates the incident state machine.
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
)

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

// NewCheck creates a new check with generated UID.
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
		Status:                    CheckStatusCreated,
		StatusStreak:              0,
		CreatedAt:                 now,
		UpdatedAt:                 now,
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
	Labels          map[string]string // key:value pairs for AND filtering
	CheckGroupUID   *string           // filter by check group UID; "none" = ungrouped checks only
	Query           string            // search term for name/slug (case-insensitive substring)
	Types           []string          // optional filter by check type (e.g. ["ssh"]); empty = every type
	Internal        *string           // "true", "false", or "all" — filter by internal status
	Statuses        []CheckStatus     // optional filter by current status (up/down/etc.)
	Limit           int               // max results to return (0 = no limit)
	CursorCreatedAt *time.Time        // cursor: created_at of last item from previous page
	CursorUID       *string           // cursor: uid of last item from previous page

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
