package heartbeat_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

const testToken = "s3cr3t-token"

// heartbeatSetup spins up an in-memory sqlite world with one organization and
// one heartbeat-type check carrying testToken, mirroring incidents'
// flapSetup pattern.
type heartbeatSetup struct {
	svc   *heartbeat.Service
	dbSvc *sqlite.Service
	org   *models.Organization
	check *models.Check
}

func newHeartbeatSetup(t *testing.T) *heartbeatSetup {
	t.Helper()
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := heartbeat.NewService(dbSvc, jobs, nil, nil, 0)

	org := models.NewOrganization("heartbeat-test", "Heartbeat Test")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "cron-job", string(checkerdef.CheckTypeHeartbeat))
	check.Config["token"] = testToken
	r.NoError(dbSvc.CreateCheck(ctx, check))

	return &heartbeatSetup{svc: svc, dbSvc: dbSvc, org: org, check: check}
}

// lastOutput fetches the most recently persisted raw result's Output map for
// the setup's check.
func (s *heartbeatSetup) lastOutput(t *testing.T) models.JSONMap {
	t.Helper()

	return s.lastResult(t).Output
}

// lastResult fetches the most recently persisted raw result for the setup's
// check.
func (s *heartbeatSetup) lastResult(t *testing.T) *models.Result {
	t.Helper()

	results, err := s.dbSvc.GetLastResultForChecks(t.Context(), s.org.UID, []string{s.check.UID})
	require.NoError(t, err)
	require.Contains(t, results, s.check.UID)

	return results[s.check.UID]
}

func TestReceiveHeartbeatPersistsCallerMetadata(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, testToken, "up", "", 0,
		"my-cron/1.0", "203.0.113.7", "POST", nil,
	)
	r.NoError(err)

	output := s.lastOutput(t)
	r.Equal("my-cron/1.0", output["userAgent"])
	r.Equal("203.0.113.7", output["remoteAddr"])
	r.Equal("POST", output["httpMethod"])
	r.Equal("Heartbeat received", output["message"])
}

func TestReceiveHeartbeatOmitsAbsentCallerMetadata(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	// No User-Agent header, no proxy headers — handler would pass "" for
	// userAgent and base.ExtractRemoteAddr's own "unknown" fallback for
	// remoteAddr when there's truly nothing to go on. Here we simulate the
	// no-User-Agent case directly: blank in, absent key out.
	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, testToken, "up", "", 0,
		"", "198.51.100.9", "GET", nil,
	)
	r.NoError(err)

	output := s.lastOutput(t)
	r.NotContains(output, "userAgent", "blank User-Agent must be omitted, not stored as an empty string")
	r.Equal("198.51.100.9", output["remoteAddr"])
	r.Equal("GET", output["httpMethod"])
}

func TestReceiveHeartbeatOmitsAllCallerMetadataWhenBlank(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	err := s.svc.ReceiveHeartbeat(t.Context(), s.org.Slug, s.check.UID, testToken, "up", "", 0, "", "", "", nil)
	r.NoError(err)

	output := s.lastOutput(t)
	r.NotContains(output, "userAgent")
	r.NotContains(output, "remoteAddr")
	r.NotContains(output, "httpMethod")
	r.Equal("Heartbeat received", output["message"], "message must persist unaffected by absent caller metadata")
}

func TestReceiveHeartbeatCallerMetadataDoesNotBypassValidation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, "wrong-token", "up", "", 0,
		"my-cron/1.0", "203.0.113.7", "POST", nil,
	)
	r.ErrorIs(err, heartbeat.ErrInvalidToken)
}

func TestReceiveHeartbeatPersistsCallerDataNested(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	callerData := map[string]any{"runId": "42", "recordCount": float64(7)}

	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, testToken, "up", "custom message", 0,
		"my-cron/1.0", "203.0.113.7", "POST", callerData,
	)
	r.NoError(err)

	output := s.lastOutput(t)
	r.Equal("custom message", output["message"])
	data, ok := output["data"].(map[string]any)
	r.True(ok, "data must be present and a JSONMap")
	r.Equal("42", data["runId"])
	r.InEpsilon(float64(7), data["recordCount"], 0.0001)
}

func TestReceiveHeartbeatOmitsDataKeyWhenCallerDataEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, testToken, "up", "", 0,
		"my-cron/1.0", "203.0.113.7", "POST", nil,
	)
	r.NoError(err)

	output := s.lastOutput(t)
	r.NotContains(output, "data", "no data key at all when callerData is empty, not an empty object")
}

func TestReceiveHeartbeatCallerDataCannotForgeServerCapturedFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	// A caller-supplied body claiming remoteAddr/userAgent/httpMethod must
	// never overwrite the server-observed values — those keys stay confined
	// inside "data".
	forgedData := map[string]any{
		"remoteAddr": "6.6.6.6",
		"userAgent":  "evil/1.0",
		"httpMethod": "DELETE",
	}

	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, testToken, "up", "", 0,
		"my-cron/1.0", "203.0.113.7", "POST", forgedData,
	)
	r.NoError(err)

	output := s.lastOutput(t)
	r.Equal("my-cron/1.0", output["userAgent"], "server-captured userAgent must win over forged data")
	r.Equal("203.0.113.7", output["remoteAddr"], "server-captured remoteAddr must win over forged data")
	r.Equal("POST", output["httpMethod"], "server-captured httpMethod must win over forged data")

	data, ok := output["data"].(map[string]any)
	r.True(ok)
	r.Equal("6.6.6.6", data["remoteAddr"], "forged value lands harmlessly inside data")
	r.Equal("evil/1.0", data["userAgent"])
	r.Equal("DELETE", data["httpMethod"])
}

func TestReceiveHeartbeatPersistsDurationOnResult(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, testToken, "up", "", 42_000,
		"my-cron/1.0", "203.0.113.7", "POST", nil,
	)
	r.NoError(err)

	result := s.lastResult(t)
	r.NotNil(result.Duration)
	r.InEpsilon(float32(42_000), *result.Duration, 0.0001)
}

func TestReceiveHeartbeatDefaultsDurationToZeroWhenAbsent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, testToken, "up", "", 0,
		"my-cron/1.0", "203.0.113.7", "POST", nil,
	)
	r.NoError(err)

	result := s.lastResult(t)
	r.NotNil(result.Duration)
	r.InDelta(float32(0), *result.Duration, 0.0001)
}

func TestReceiveHeartbeatPersistsDurationOnRunningStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	// The spec calls out that "running" gets no special casing: an unusual
	// but present durationMs on a run-start ping is still stored.
	err := s.svc.ReceiveHeartbeat(
		t.Context(), s.org.Slug, s.check.UID, testToken, "running", "", 1_500,
		"my-cron/1.0", "203.0.113.7", "POST", nil,
	)
	r.NoError(err)

	result := s.lastResult(t)
	r.NotNil(result.Duration)
	r.InEpsilon(float32(1_500), *result.Duration, 0.0001)
}
