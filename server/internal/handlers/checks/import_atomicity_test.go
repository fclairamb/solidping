package checks_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// errLabelWriteBoom is the injected post-insert failure. It is deliberately
// NOT a validation error: with spec 2026-09-10-01's up-front label validation
// in place, the only failures that can still land after the check row exists
// are infrastructure ones, and those are exactly what the compensating delete
// has to cover.
var errLabelWriteBoom = errors.New("injected label write failure")

// poisonLabelDB wraps a real db.Service and fails the label write for the ONE
// check carrying a sentinel label value — after the check row has already been
// inserted, which is the window the incident happened in.
//
// Precedent for the shape: barrierIncidentsDB in
// internal/handlers/availability/service_test.go. Wrapping the interface is
// what makes "a failure lands mid-item" a deterministic fact rather than
// something you hope to reproduce.
type poisonLabelDB struct {
	db.Service

	poisonValue string
	// failPurge makes the COMPENSATING delete fail too — the branch where a
	// row really does survive and the import must say so.
	failPurge bool

	mu sync.Mutex
	// armed is set when the poisoned label is resolved, so the very next
	// SetCheckLabels (the same check's, since attachLabels does the two calls
	// back to back) is the one that fails.
	armed bool
	// failedCheckUID records which check the failure landed on, so the test
	// can assert on rows keyed by it.
	failedCheckUID string
	purgeAttempted bool
}

func (p *poisonLabelDB) GetOrCreateLabel(
	ctx context.Context, orgUID, key, value string,
) (*models.Label, error) {
	if value == p.poisonValue {
		p.mu.Lock()
		p.armed = true
		p.mu.Unlock()
	}

	return p.Service.GetOrCreateLabel(ctx, orgUID, key, value)
}

func (p *poisonLabelDB) SetCheckLabels(ctx context.Context, checkUID string, labelUIDs []string) error {
	p.mu.Lock()
	armed := p.armed
	if armed {
		p.armed = false
		p.failedCheckUID = checkUID
	}
	p.mu.Unlock()

	if armed {
		return errLabelWriteBoom
	}

	return p.Service.SetCheckLabels(ctx, checkUID, labelUIDs)
}

func (p *poisonLabelDB) PurgeCheck(ctx context.Context, uid string) error {
	p.mu.Lock()
	p.purgeAttempted = true
	failPurge := p.failPurge
	p.mu.Unlock()

	if failPurge {
		return errLabelWriteBoom
	}

	return p.Service.PurgeCheck(ctx, uid)
}

func (p *poisonLabelDB) failedUID() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.failedCheckUID
}

// poisonDoc is a three-check document where the middle one carries the
// sentinel label value, plus a group so the group link can be verified too.
func poisonDoc(org, poisonValue string) map[string]any {
	one := httpCheck("atom-one", map[string]string{"environment": "prod"})
	two := httpCheck("atom-two", map[string]string{"environment": poisonValue})
	three := httpCheck("atom-three", map[string]string{"environment": "staging"})

	for _, entry := range []map[string]any{one, two, three} {
		entry["group"] = "Acme Platform"
	}

	return importDocument(org, one, two, three)
}

// TestImportMidItemFailureLeavesNoCheckBehind is regression test 3 of spec
// 2026-09-10-01. The real incident created 47 checks and reported
// `created: 0`: CreateCheck inserts the check row first and only then writes
// labels, so a label failure returned an error while the row stayed.
func TestImportMidItemFailureLeavesNoCheckBehind(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	var poison *poisonLabelDB

	rig := newLabelRig(t, "atomicity", func(inner db.Service) db.Service {
		poison = &poisonLabelDB{Service: inner, poisonValue: "boom"}

		return poison
	})

	result := rig.importDoc(t, poisonDoc(rig.org.Slug, "boom"), false)

	// Honest counts: two really were created, one really failed.
	r.Equal(2, result.Created)
	r.Equal(0, result.Updated)
	r.Len(result.Errors, 1)
	r.Equal("atom-two", result.Errors[0].Slug)
	r.Empty(result.Errors[0].State, "the compensating delete worked, so nothing is left over")

	// The failed slug resolves to nothing — a SOFT delete would have kept the
	// slug claimed and this would still find a row. Both backends answer a
	// missing row with sql.ErrNoRows and a nil check, so both halves are
	// asserted unconditionally: a guarded assertion here would pass even if
	// the row survived.
	orphan, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "atom-two")
	r.ErrorIs(err, sql.ErrNoRows, "the failed check's slug must be free again")
	r.Nil(orphan, "the failed check must not exist")

	r.Equal(2, rig.countChecks(t))

	// ... and no scheduler row survived it either.
	r.True(poison.purgeAttempted)
	r.NotEmpty(poison.failedUID())

	jobs, err := rig.dbSvc.ListCheckJobsByCheckUID(ctx, poison.failedUID())
	r.NoError(err)
	r.Empty(jobs, "the half-created check's check_jobs must be gone too")

	// No check.created event was emitted for it either — a consumer of the
	// event stream must not be told about a check that does not exist.
	failedUID := poison.failedUID()
	events, err := rig.dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: rig.org.UID,
		CheckUID:        &failedUID,
		EventTypes:      []models.EventType{models.EventTypeCheckCreated},
	})
	r.NoError(err)
	r.Empty(events, "a check that was rolled back must not have announced itself")

	// Positive control on that assertion: the checks that DID succeed emitted
	// their check.created event, so the absence above means something.
	survivor, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "atom-one")
	r.NoError(err)

	events, err = rig.dbSvc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: rig.org.UID,
		CheckUID:        &survivor.UID,
		EventTypes:      []models.EventType{models.EventTypeCheckCreated},
	})
	r.NoError(err)
	r.NotEmpty(events)

	// Group link check (the incident report claimed the created checks had
	// none): the two that succeeded carry the auto-created group.
	groups, err := rig.dbSvc.ListCheckGroups(ctx, rig.org.UID)
	r.NoError(err)
	r.Len(groups, 1)

	for _, slug := range []string{"atom-one", "atom-three"} {
		check, checkErr := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, slug)
		r.NoError(checkErr)
		r.NotNil(check.CheckGroupUID, "%s must be linked to the imported group", slug)
		r.Equal(groups[0].UID, *check.CheckGroupUID)
	}

	// Re-importing the corrected document creates the missing one and leaves
	// the two that made it untouched — which is only possible because the
	// failed slug was really released.
	fixed := rig.importDoc(t, poisonDoc(rig.org.Slug, "prod"), false)
	r.Empty(fixed.Errors, "%+v", fixed.Errors)
	r.Equal(1, fixed.Created)
	r.Equal(0, fixed.Updated)
	r.Equal(2, fixed.Unchanged)
	r.Equal(3, rig.countChecks(t))
}

// TestImportReportsCreatedIncompleteWhenCompensationFails covers the branch
// where the compensating delete ITSELF fails. A row really is on disk, so the
// one thing the response must not do is report `created: 0` and move on — the
// caller has to be told which slug exists.
func TestImportReportsCreatedIncompleteWhenCompensationFails(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	var poison *poisonLabelDB

	rig := newLabelRig(t, "atomicity-stuck", func(inner db.Service) db.Service {
		poison = &poisonLabelDB{Service: inner, poisonValue: "boom", failPurge: true}

		return poison
	})

	result := rig.importDoc(t, poisonDoc(rig.org.Slug, "boom"), false)

	r.Equal(2, result.Created)
	r.Len(result.Errors, 1)
	r.Equal("atom-two", result.Errors[0].Slug)
	r.Equal(checks.ImportStateCreatedIncomplete, result.Errors[0].State,
		"a row that survived must be reported as created-incomplete, never silently as zero")

	// And the row really is there — the report is telling the truth.
	orphan, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "atom-two")
	r.NoError(err)
	r.NotNil(orphan)
	r.Equal(3, rig.countChecks(t))

	_ = poison
}
