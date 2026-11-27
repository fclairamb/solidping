package agentws_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// Spec 2026-11-27-04: an agent older than v0.56.0 sends its results with the
// resolved `${param:…}` values in clear. The server resolved them for the
// claim, so it masks them again before storing the result.

const maskedToken = "acmetoken42"

// createConfigCheck creates a check in the private test region with the given
// public config.
func (e *env) createConfigCheck(slug string, config models.JSONMap) *models.Check {
	e.t.Helper()

	check := models.NewCheck(e.org.UID, slug, "http")
	check.Config = config
	check.Regions = []string{testRegion}
	check.ConfirmationPeriodSeconds = 0
	require.NoError(e.t, e.dbSvc.CreateCheck(e.t.Context(), check))

	return check
}

// claimJobs enrolls an agent, claims every due job and returns the
// connection with the job UIDs keyed by check UID.
func (e *env) claimJobs() (map[string]string, func(frame agentcrypto.ClientFrame) agentcrypto.ServerFrame) {
	e.t.Helper()

	conn, _, _ := e.enroll(e.mintToken(), "dc1-agent")

	jobsResp := roundTrip(e.t, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10})
	require.Equal(e.t, agentcrypto.MsgTypeJobs, jobsResp.Type)

	jobs := make(map[string]string, len(jobsResp.Jobs))
	for _, job := range jobsResp.Jobs {
		jobs[job.CheckUID] = job.UID
	}

	return jobs, func(frame agentcrypto.ClientFrame) agentcrypto.ServerFrame {
		return roundTrip(e.t, conn, frame)
	}
}

func (e *env) storedResult(checkUID string) *models.Result {
	e.t.Helper()

	results, err := e.dbSvc.GetLastResultForChecks(e.t.Context(), e.org.UID, []string{checkUID})
	require.NoError(e.t, err)

	got, ok := results[checkUID]
	require.True(e.t, ok, "the result must be stored")

	return got
}

func leakyFrame(jobUID string) agentcrypto.ClientFrame {
	return agentcrypto.ClientFrame{
		Type:   agentcrypto.MsgTypeResult,
		ID:     "r-" + jobUID,
		JobUID: jobUID,
		Status: int(models.ResultStatusDown),
		Output: map[string]any{
			"error": `parse "https://acme.com/x?k=` + maskedToken + `": invalid character`,
			"redirects": map[string]any{
				"final": []any{"https://acme.com/x?k=" + maskedToken},
			},
			"note": "nothing secret here",
		},
		Metrics: map[string]any{maskedToken + "_ms": 12.5, "total_ms": 30.0},
	}
}

func jsonOf(t *testing.T, value any) string {
	t.Helper()

	blob, err := json.Marshal(value)
	require.NoError(t, err)

	return string(blob)
}

func TestAgentResultIsMaskedServerSide(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	r.NoError(e.dbSvc.SetOrgParameter(t.Context(), e.org.UID, paramkeys.StorageKey("token"), maskedToken, true))

	refCheck := e.createConfigCheck("with-ref", models.JSONMap{"url": "https://acme.com/x?k=${param:token}"})
	plainCheck := e.createConfigCheck("no-ref", models.JSONMap{"url": "https://acme.com/x"})

	jobs, send := e.claimJobs()
	r.Len(jobs, 2)

	// A resolved parameter value is masked before storage.
	{
		ack := send(leakyFrame(jobs[refCheck.UID]))
		r.Equal(agentcrypto.MsgTypeAck, ack.Type)

		got := e.storedResult(refCheck.UID)
		r.NotContains(jsonOf(t, got.Output), maskedToken)
		r.NotContains(jsonOf(t, got.Metrics), maskedToken)

		r.Equal(`parse "https://acme.com/x?k=***": invalid character`, got.Output["error"])
		r.Equal(map[string]any{"final": []any{"https://acme.com/x?k=***"}}, got.Output["redirects"])
		r.Equal("nothing secret here", got.Output["note"], "an unrelated string is left alone")
		r.Contains(got.Metrics, "***_ms")
		r.Contains(got.Metrics, "total_ms")
	}

	// A job with no reference is stored as sent.
	{
		frame := leakyFrame(jobs[plainCheck.UID])
		ack := send(frame)
		r.Equal(agentcrypto.MsgTypeAck, ack.Type)

		got := e.storedResult(plainCheck.UID)
		r.JSONEq(jsonOf(t, frame.Output), jsonOf(t, got.Output))
		r.JSONEq(jsonOf(t, frame.Metrics), jsonOf(t, got.Metrics))
	}
}

// TestAgentResultWithoutParameterValueIsUnchanged is the positive control on
// a job that does hold a reference: an output carrying no parameter value is
// stored byte for byte.
func TestAgentResultWithoutParameterValueIsUnchanged(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	r.NoError(e.dbSvc.SetOrgParameter(t.Context(), e.org.UID, paramkeys.StorageKey("token"), maskedToken, true))

	check := e.createConfigCheck("with-ref", models.JSONMap{"url": "https://acme.com/x?k=${param:token}"})
	jobs, send := e.claimJobs()

	frame := agentcrypto.ClientFrame{
		Type:    agentcrypto.MsgTypeResult,
		ID:      "r1",
		JobUID:  jobs[check.UID],
		Status:  int(models.ResultStatusDown),
		Output:  map[string]any{"error": "connection refused", "url": "https://acme.com/x?k=***"},
		Metrics: map[string]any{"total_ms": 30.0},
	}
	r.Equal(agentcrypto.MsgTypeAck, send(frame).Type)

	got := e.storedResult(check.UID)
	r.JSONEq(jsonOf(t, frame.Output), jsonOf(t, got.Output))
	r.JSONEq(jsonOf(t, frame.Metrics), jsonOf(t, got.Metrics))
}

// TestAgentResultStoredWhenParameterIsGone is the failure path: a parameter
// deleted since the claim cannot be recomputed, and the result is stored as
// sent rather than dropped.
func TestAgentResultStoredWhenParameterIsGone(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	// Never seeded: the reference does not resolve at receipt.
	check := e.createConfigCheck("orphan-ref", models.JSONMap{"url": "https://acme.com/x?k=${param:token}"})
	jobs, send := e.claimJobs()

	frame := leakyFrame(jobs[check.UID])
	r.Equal(agentcrypto.MsgTypeAck, send(frame).Type, "the result is not dropped")

	got := e.storedResult(check.UID)
	r.JSONEq(jsonOf(t, frame.Output), jsonOf(t, got.Output))
}
