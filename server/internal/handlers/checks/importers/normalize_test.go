package importers_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

// The `timeout` config key is uniform across every check type: the checks
// service range-checks it on write (checks.Min/MaxConfigTimeout) and the worker
// reads it straight off the config map to size the execution budget
// (checkworker.perCheckTimeout, covered by worker_test.go). It is therefore
// meaningful even for checkers whose own config struct has no Timeout field —
// but a foreign value outside 1s–30s would fail the whole check on import, so
// the converters clamp it and warn. Same story for the confirmation period,
// which the service caps at one day.

// TestKumaDefaultTimeoutIsClampedNotRejected guards the single most common real
// import: Uptime Kuma ships a 48s default timeout on every HTTP monitor, which
// is above SolidPing's 30s cap. Before the clamp this failed the check outright
// with `timeout: must be >= 1s and <= 30s`.
func TestKumaDefaultTimeoutIsClampedNotRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	body := []byte(`{"version":"1.23.13","monitorList":[
	  {"id":1,"name":"Kuma defaults","type":"http","url":"https://a.example.org","active":true,
	   "interval":60,"timeout":48,"maxretries":3,"retryInterval":86400}
	]}`)

	result := decodeConvert(t, harness.post(t, "uptime-kuma", false, body))

	// The check imported cleanly instead of erroring out…
	r.Empty(result.Errors)
	r.Equal(1, result.Created)

	// …and both out-of-range values were reported, not silently mangled.
	r.True(warningMentions(result.Warnings, "clamped to 30s"), "%+v", result.Warnings)
	r.True(warningMentions(result.Warnings, "exceeds SolidPing's 24h maximum"), "%+v", result.Warnings)

	// The clamped timeout is really persisted on the check.
	check, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "kuma-defaults")
	r.NoError(err)
	r.Equal("30s", check.Config["timeout"])
}

// TestBetterStackLongGraceIsClampedNotRejected covers the same failure on the
// Better Stack side, where a heartbeat grace period of several days is normal.
func TestBetterStackLongGraceIsClampedNotRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newBetterStackServer(t)
	harness := newConvertHarness(t, srv.URL)

	result := decodeConvert(t, harness.post(t, "betterstack", false, betterStackBody(t, "")))

	r.Empty(result.Errors)
	r.True(warningMentions(result.Warnings, "exceeds SolidPing's 24h maximum"), "%+v", result.Warnings)

	// 172800s (2 days) of grace became the 86400s cap.
	check, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "weekly-report")
	r.NoError(err)
	r.Equal(86400, check.ConfirmationPeriodSeconds)
}

// TestGatusOverCapTimeoutIsClampedNotRejected covers the Gatus client.timeout
// path, which feeds the same uniform config key.
func TestGatusOverCapTimeoutIsClampedNotRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	result := decodeConvert(t, harness.post(t, "gatus", false, readFixture(t, "gatus-config.yaml")))

	r.Empty(result.Errors)
	r.True(warningMentions(result.Warnings, "clamped to 30s"), "%+v", result.Warnings)

	check, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "legacy-soap")
	r.NoError(err)
	r.Equal("30s", check.Config["timeout"])
}

// TestUptimeRobotOverCapTimeoutIsClampedNotRejected covers the UptimeRobot
// `timeout` field, which feeds the same uniform config key. UptimeRobot allows
// per-monitor timeouts well past SolidPing's 30s cap.
func TestUptimeRobotOverCapTimeoutIsClampedNotRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	body := []byte(`{"monitors":[
	  {"id":1,"friendly_name":"Slow endpoint","url":"https://slow.example.org","type":1,
	   "interval":300,"timeout":45,"status":2}
	]}`)

	result := decodeConvert(t, harness.post(t, "uptimerobot", false, body))

	r.Empty(result.Errors)
	r.True(warningMentions(result.Warnings, "clamped to 30s"), "%+v", result.Warnings)

	check, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "slow-endpoint")
	r.NoError(err)
	r.Equal("30s", check.Config["timeout"])
}

// TestInRangeTimeoutSurvivesTheImport is the positive control for the clamp:
// a timeout already inside the range must reach the persisted check untouched,
// so the tests above cannot pass by simply dropping the key.
func TestInRangeTimeoutSurvivesTheImport(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := newConvertHarness(t, "")
	body := []byte(`{"version":"1.23.13","monitorList":[
	  {"id":1,"name":"Sane timeout","type":"http","url":"https://a.example.org","active":true,
	   "interval":60,"timeout":12,"maxretries":1,"retryInterval":30}
	]}`)

	result := decodeConvert(t, harness.post(t, "uptime-kuma", false, body))
	r.Empty(result.Errors)

	check, err := harness.dbSvc.GetCheckByUidOrSlug(t.Context(), harness.org.UID, "sane-timeout")
	r.NoError(err)
	r.Equal("12s", check.Config["timeout"], "an in-range timeout must be carried over verbatim")
	r.Equal(30, check.ConfirmationPeriodSeconds)

	// And no clamp warning was invented for a value that needed no clamping.
	r.False(warningMentions(result.Warnings, "clamped"), "%+v", result.Warnings)
}

// TestUnparseableTimeoutIsDroppedWithAWarning covers the defensive branch.
func TestUnparseableTimeoutIsDroppedWithAWarning(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.GatusConverter{}
	result, err := conv.Convert([]byte(`
endpoints:
  - name: bad timeout
    url: "https://a.example.org"
    client:
      timeout: not-a-duration
    conditions: ["[STATUS] == 200"]
`))
	r.NoError(err)
	r.Len(result.Document.Checks, 1)
	r.NotContains(result.Document.Checks[0].Config, "timeout")
	r.True(warningMentions(result.Warnings, "unparseable timeout"), "%+v", result.Warnings)
}
