// Package regionquorum decides how many of a check's regions must agree
// before the check is down (spec 2026-09-25-10).
//
// A check that runs from N regions used to go through one state machine that
// ignored the region: any passing result from any region cleared the
// confirmation clock, so an incident opened only when every region failed for
// the whole window, and a real regional failure was never surfaced. The
// `failQuorum` setting makes that rule explicit:
//
//   - default: all regions for N <= 2 (the old behavior), a majority for N >= 3;
//   - "all": every region, which KEEPS the old per-result behavior exactly;
//   - "majority": floor(N/2)+1;
//   - an integer k: k regions, clamped to N.
//
// When the resolved quorum Q is lower than N the check is in quorum mode: the
// incident pipeline derives its signal from the per-region states instead of
// from the result it is processing. This package is the one place both the
// resolution and the evaluation live, so the incident engine, the API's
// regional-issue block and MCP can never disagree.
package regionquorum

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Keywords accepted for failQuorum.
const (
	// KeywordDefault is the wire value of an unset failQuorum.
	KeywordDefault = "default"
	// KeywordAll requires every region (the historical behavior).
	KeywordAll = "all"
	// KeywordMajority requires floor(N/2)+1 regions.
	KeywordMajority = "majority"
)

// MaxCount bounds an explicit integer quorum. Far above any real region count,
// it only exists so a typo cannot store an absurd number.
const MaxCount = 100

// majorityFrom is the region count from which the default switches from "all"
// to "majority".
const majorityFrom = 3

// ErrInvalid is returned for a failQuorum that is none of the accepted forms.
var ErrInvalid = errors.New(
	`failQuorum must be "default", "all", "majority" or a whole number of regions between 1 and 100`,
)

// Kind is the shape of a failQuorum setting.
type Kind int

const (
	// KindDefault is an unset setting.
	KindDefault Kind = iota
	// KindAll requires every region.
	KindAll
	// KindMajority requires floor(N/2)+1 regions.
	KindMajority
	// KindCount requires an explicit number of regions.
	KindCount
)

// Setting is a parsed failQuorum.
type Setting struct {
	Kind  Kind
	Count int
}

// Parse reads a failQuorum value. The empty string and "default" are the
// default; keywords are case-insensitive.
func Parse(raw string) (Setting, error) {
	value := strings.ToLower(strings.TrimSpace(raw))

	switch value {
	case "", KeywordDefault:
		return Setting{Kind: KindDefault}, nil
	case KeywordAll:
		return Setting{Kind: KindAll}, nil
	case KeywordMajority:
		return Setting{Kind: KindMajority}, nil
	}

	count, err := strconv.Atoi(value)
	if err != nil || count < 1 || count > MaxCount {
		return Setting{}, ErrInvalid
	}

	return Setting{Kind: KindCount, Count: count}, nil
}

// FromStored reads the checks.fail_quorum column. A value the column CHECK
// would refuse cannot be stored; should one appear anyway it reads as the
// default rather than failing a result's processing.
func FromStored(stored *string) Setting {
	if stored == nil {
		return Setting{Kind: KindDefault}
	}

	setting, err := Parse(*stored)
	if err != nil {
		return Setting{Kind: KindDefault}
	}

	return setting
}

// Stored is the column value: nil for the default.
func (s Setting) Stored() *string {
	if s.Kind == KindDefault {
		return nil
	}

	value := s.String()

	return &value
}

// String is the canonical text form: "default", "all", "majority" or "3".
func (s Setting) String() string {
	switch s.Kind {
	case KindAll:
		return KeywordAll
	case KindMajority:
		return KeywordMajority
	case KindCount:
		return strconv.Itoa(s.Count)
	case KindDefault:
		return KeywordDefault
	}

	return KeywordDefault
}

// Wire is the API value of the setting.
func (s Setting) Wire() Value {
	return Value(s.String())
}

// Resolve returns the effective quorum Q for a check running from n regions.
// 0 when n is 0 (a passive or regionless check has no quorum).
func (s Setting) Resolve(n int) int {
	if n <= 0 {
		return 0
	}

	switch s.Kind {
	case KindAll:
		return n
	case KindMajority:
		return n/2 + 1
	case KindCount:
		return min(s.Count, n)
	case KindDefault:
		if n < majorityFrom {
			return n
		}

		return n/2 + 1
	}

	return n
}

// UsesQuorum reports whether a check with n regions and an effective quorum q
// is evaluated from its per-region states. Otherwise (one region, or a quorum
// of every region) the per-result state machine applies unchanged: it IS the
// "all regions agree" rule, and it keeps every existing single- and
// dual-region check's incident timing exactly as it was.
func UsesQuorum(q, n int) bool {
	return n >= 2 && q >= 1 && q < n
}

// ForCheck resolves a check's setting against its current regions: the
// setting, the region count N and the effective quorum Q. A passive check has
// no regions, so N and Q are 0.
func ForCheck(check *models.Check) (Setting, int, int) {
	setting := FromStored(check.FailQuorum)
	n := RegionCount(check)

	return setting, n, setting.Resolve(n)
}

// RegionCount is N: the number of distinct regions the check runs from right
// now. 0 for a passive check.
func RegionCount(check *models.Check) int {
	if check.IsPassive() {
		return 0
	}

	return len(distinct(check.Regions))
}

// Value is failQuorum on the wire: "default", "all", "majority" or a positive
// integer. It decodes a JSON string or a JSON number (`2` and `"2"` are the
// same value) and encodes a count as a number, the keywords as strings.
//
// Decoding never fails on a well-formed JSON scalar: whatever arrives is kept
// as text so validation can refuse it as a field finding (400 with the field
// named) rather than as an opaque body-decode error.
type Value string

// UnmarshalJSON accepts a JSON string or any other JSON scalar.
func (v *Value) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err //nolint:wrapcheck // the decoder's own error is the useful one
		}

		*v = Value(text)

		return nil
	}

	*v = Value(string(trimmed))

	return nil
}

// MarshalJSON writes a count as a JSON number and anything else as a string.
func (v Value) MarshalJSON() ([]byte, error) {
	if count, err := strconv.Atoi(string(v)); err == nil {
		return []byte(strconv.Itoa(count)), nil
	}

	return json.Marshal(string(v)) //nolint:wrapcheck // a string always encodes
}

// Setting parses the wire value.
func (v Value) Setting() (Setting, error) {
	return Parse(string(v))
}

// RegionState is one region's newest reading, as the evaluation sees it.
type RegionState struct {
	Region       string
	Status       models.ResultStatus
	Since        time.Time
	LastResultAt time.Time
}

// StatesOf converts stored per-region rows for Evaluate. The incident engine
// and the read paths (checks API, MCP) all go through it, so they evaluate
// the same thing.
func StatesOf(rows []models.CheckRegionState) []RegionState {
	out := make([]RegionState, 0, len(rows)+1)

	for i := range rows {
		out = append(out, RegionState{
			Region:       rows[i].Region,
			Status:       rows[i].Status,
			Since:        rows[i].StatusSince,
			LastResultAt: rows[i].LastResultAt,
		})
	}

	return out
}

// Evaluation is the quorum's view of a check's current regions.
type Evaluation struct {
	// Regions is N, the check's current region count.
	Regions int
	// Quorum is Q, the effective quorum.
	Quorum int
	// Failing, Passing and Unknown partition the check's CURRENT regions, in
	// the check's own region order. Unknown is a region with no reading yet
	// (just placed) or whose newest reading is older than the staleness
	// threshold: it counts as neither failing nor passing.
	Failing []string
	Passing []string
	Unknown []string
}

// QuorumFailing reports whether at least Q current regions are failing.
func (e *Evaluation) QuorumFailing() bool {
	return e.Quorum > 0 && len(e.Failing) >= e.Quorum
}

// RegionalIssue reports whether some, but fewer than Q, regions are failing:
// the "regional issue" warning, visible but never an incident.
func (e *Evaluation) RegionalIssue() bool {
	return len(e.Failing) > 0 && !e.QuorumFailing()
}

// Evaluate partitions the check's current regions by their newest reading.
//
// Only regions listed in `regions` count: a stored state for a region the
// check has left (an automatic re-placement swapped it out) is ignored — the
// resolved rule is "quorum is keyed to the check's current region set", which
// cannot drift if a placement event is missed or reordered. A state whose
// reading is older than staleAfter is ignored too (0 disables that filter).
func Evaluate(
	regions []string, states []RegionState, quorum int, now time.Time, staleAfter time.Duration,
) Evaluation {
	current := distinct(regions)
	byRegion := make(map[string]RegionState, len(states))

	for i := range states {
		state := &states[i]
		if previous, ok := byRegion[state.Region]; ok && previous.LastResultAt.After(state.LastResultAt) {
			continue
		}

		byRegion[state.Region] = *state
	}

	eval := Evaluation{Regions: len(current), Quorum: quorum}

	for _, region := range current {
		state, ok := byRegion[region]
		switch {
		case !ok || (staleAfter > 0 && now.Sub(state.LastResultAt) > staleAfter):
			eval.Unknown = append(eval.Unknown, region)
		case state.Status.IsFailure():
			eval.Failing = append(eval.Failing, region)
		default:
			eval.Passing = append(eval.Passing, region)
		}
	}

	return eval
}

// Contains reports whether region is one of the check's current regions.
func Contains(regions []string, region string) bool {
	for _, candidate := range regions {
		if candidate == region {
			return true
		}
	}

	return false
}

// distinct drops empty and repeated slugs, keeping the first occurrence.
func distinct(regions []string) []string {
	out := make([]string, 0, len(regions))
	seen := make(map[string]bool, len(regions))

	for _, region := range regions {
		if region == "" || seen[region] {
			continue
		}

		seen[region] = true
		out = append(out, region)
	}

	return out
}
