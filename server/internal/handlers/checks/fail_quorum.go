package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regionquorum"
)

// Multi-region quorum (spec 2026-09-25-10): the API, validation and
// config-as-code half of the failQuorum setting. The incident engine half
// lives in the incidents package; both resolve through regionquorum.

// CodeInvalidFailQuorum is a failQuorum that is none of "default", "all",
// "majority" or a whole number of regions between 1 and 100.
const CodeInvalidFailQuorum = "INVALID_FAIL_QUORUM"

// fieldFailQuorum is the JSON/validation field name of the setting.
const fieldFailQuorum = "failQuorum"

// errInvalidFailQuorum is the write paths' typed error, mapped to a 400.
var errInvalidFailQuorum = regionquorum.ErrInvalid

// appendFailQuorumFinding checks the failQuorum value (shared by create,
// validate, upsert, update and the import dry run through
// requestFieldFindings).
func appendFailQuorumFinding(findings []requestFieldFinding, values *requestFieldValues) []requestFieldFinding {
	if values.FailQuorum == nil {
		return findings
	}

	if _, err := values.FailQuorum.Setting(); err != nil {
		findings = append(findings, requestFieldFinding{
			Name: fieldFailQuorum, Code: CodeInvalidFailQuorum,
			Message: errInvalidFailQuorum.Error(), Err: errInvalidFailQuorum,
		})
	}

	return findings
}

// storedFailQuorum parses a request value into the column value: nil for the
// default.
func storedFailQuorum(value *regionquorum.Value) (*string, error) {
	setting, err := value.Setting()
	if err != nil {
		return nil, errInvalidFailQuorum
	}

	return setting.Stored(), nil
}

// applyFailQuorumUpdate writes a PATCH's failQuorum onto the update. The
// default travels as an explicit clear (NULL), otherwise a check moved off
// the default could never be moved back. A passive check has no regions and
// no quorum: the value is accepted and dropped, like its regions.
func applyFailQuorumUpdate(update *models.CheckUpdate, existing *models.Check, value *regionquorum.Value) error {
	if value == nil {
		return nil
	}

	stored, err := storedFailQuorum(value)
	if err != nil {
		return err
	}

	if existing.IsPassive() {
		return nil
	}

	update.FailQuorum = stored
	update.ClearFailQuorum = stored == nil

	return nil
}

// failQuorumResponse fills failQuorum / effectiveFailQuorum on a check
// response. Both are omitted for a passive check, which has no regions.
func failQuorumResponse(check *models.Check, response *CheckResponse) {
	if check.IsPassive() {
		return
	}

	setting, _, quorum := regionquorum.ForCheck(check)
	wire := setting.Wire()
	response.FailQuorum = &wire

	if quorum > 0 {
		response.EffectiveFailQuorum = &quorum
	}
}

// exportedFailQuorum is the document value: nil (absent) for the default.
func exportedFailQuorum(check *models.Check) *regionquorum.Value {
	if check.IsPassive() {
		return nil
	}

	setting := regionquorum.FromStored(check.FailQuorum)
	if setting.Kind == regionquorum.KindDefault {
		return nil
	}

	wire := setting.Wire()

	return &wire
}

// importedFailQuorum resolves a document's failQuorum to something always
// sent on the upsert: absent means the default, sent EXPLICITLY so a re-import
// moves a check that was taken off the default back onto it (a manifest
// describes the whole state, it does not "leave it unchanged").
func importedFailQuorum(value *regionquorum.Value) *regionquorum.Value {
	if value != nil {
		out := *value

		return &out
	}

	def := regionquorum.Value(regionquorum.KeywordDefault)

	return &def
}

// failQuorumOrDefault is the canonical text of a document value for the plan
// diff: absent reads as "default", an unparseable value as itself (validation
// reports it separately).
func failQuorumOrDefault(value *regionquorum.Value) string {
	if value == nil {
		return regionquorum.KeywordDefault
	}

	setting, err := value.Setting()
	if err != nil {
		return string(*value)
	}

	return setting.String()
}

// RegionalIssueResponse is the "regional issue" block of a check detail
// (spec 2026-09-25-10): some, but fewer than the quorum, of the check's
// current regions are failing. The check's status is `warning`, and no
// incident opens. Present only while that is true.
type RegionalIssueResponse struct {
	// FailingRegions are the current regions whose newest reading fails.
	FailingRegions []string `json:"failingRegions"`
	// FailQuorum is the effective quorum: how many regions must fail for the
	// check to be down.
	FailQuorum int `json:"failQuorum"`
	// RegionCount is the check's current region count.
	RegionCount int `json:"regionCount"`
}

// BuildRegionalIssue evaluates the stored readings exactly as the incident
// engine does and returns the regional-issue block, or nil. Shared with the
// MCP diagnose tool.
func BuildRegionalIssue(
	check *models.Check, rows []models.CheckRegionState, now time.Time,
) *RegionalIssueResponse {
	_, regionCount, quorum := regionquorum.ForCheck(check)
	if !regionquorum.UsesQuorum(quorum, regionCount) {
		return nil
	}

	eval := regionquorum.Evaluate(check.Regions, regionquorum.StatesOf(rows), quorum, now, check.StaleThreshold())
	if !eval.RegionalIssue() {
		return nil
	}

	return &RegionalIssueResponse{
		FailingRegions: eval.Failing,
		FailQuorum:     quorum,
		RegionCount:    regionCount,
	}
}

// DecorateRegionFreshness adds each region's newest reading (status and
// since when it has been on that side) from check_region_states to the
// per-region freshness list. Regions with no stored reading are left as they
// are: single-region checks keep none (their status is the check's own).
func DecorateRegionFreshness(list []RegionFreshnessResponse, rows []models.CheckRegionState) {
	byRegion := make(map[string]*models.CheckRegionState, len(rows))
	for i := range rows {
		byRegion[rows[i].Region] = &rows[i]
	}

	for i := range list {
		row, ok := byRegion[list[i].Region]
		if !ok {
			continue
		}

		status := regionStatusWire(row.Status)
		since := row.StatusSince
		list[i].Status = &status
		list[i].StatusSince = &since
	}
}

// regionStatusWire is the lowercase result status of a region reading.
func regionStatusWire(status models.ResultStatus) string {
	statusInt := int(status)

	return resultStatusString(&models.Result{Status: &statusInt})
}

// attachRegionStates reads the check's per-region readings once and uses
// them for both the freshness decoration and the regional-issue block.
func (s *Service) attachRegionStates(ctx context.Context, check *models.Check, response *CheckResponse) error {
	if check.IsPassive() {
		return nil
	}

	rows, err := s.db.ListCheckRegionStates(ctx, check.UID)
	if err != nil {
		return fmt.Errorf("failed to get region states: %w", err)
	}

	DecorateRegionFreshness(response.RegionFreshness, rows)
	response.RegionalIssue = BuildRegionalIssue(check, rows, s.now())

	return nil
}
