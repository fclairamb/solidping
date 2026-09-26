package watchdog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// Private regions in the dark-region detector (spec 2026-09-25-01). A private
// region (`@<slug>`) is org-relative and served by that org's agents, so the
// watchdog must (a) not call it dark while its agent is connected, and (b)
// never let two orgs' same-named private region share one anomaly.

// addOrg creates a second organization next to env.org.
func (e *testEnv) addOrg(t *testing.T, slug string) *models.Organization {
	t.Helper()

	org := models.NewOrganization(slug, slug)
	require.NoError(t, e.db.CreateOrganization(t.Context(), org))

	return org
}

// createOrgCheck is createCheck for an arbitrary org.
func (e *testEnv) createOrgCheck(t *testing.T, orgSlug string, regionSlugs []string) string {
	t.Helper()

	resp, err := e.checks.CreateCheck(t.Context(), orgSlug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: regionSlugs,
		Period:  ptr("00:01:00"),
	})
	require.NoError(t, err)

	return resp.UID
}

// privateX is the private region slug every fixture here strands or serves.
const privateX = "@x"

// strandPrivateRegion creates n checks on privateX in orgSlug with every job
// three hours overdue — past both the width and the critical-age bars.
func (e *testEnv) strandPrivateRegion(t *testing.T, orgSlug string, n int) {
	t.Helper()

	overdue := time.Now().Add(-3 * time.Hour)

	for range n {
		checkUID := e.createOrgCheck(t, orgSlug, []string{privateX})
		e.setJobScheduledAt(t, checkUID, overdue)
	}
}

// insertLiveOrgAgent writes an active org agent bound to region, last seen
// now (RegionHealth reads the real clock, see registerLiveWorker).
func (e *testEnv) insertLiveOrgAgent(t *testing.T, orgUID, region string) {
	t.Helper()

	seen := time.Now()
	agent := models.NewAgent(orgUID, region, "agent", "", "age1test", "fp")
	agent.Ed25519PublicKey = "ed-" + agent.UID
	agent.LastSeenAt = &seen

	_, err := e.db.DB().NewInsert().Model(agent).Exec(t.Context())
	require.NoError(t, err)
}

// TestDarkRegionDetectorKeysPrivateRegionsPerOrg: two orgs with a dark `@x`
// produce two anomalies with distinct, org-qualified subjects.
func TestDarkRegionDetectorKeysPrivateRegionsPerOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	bravo := env.addOrg(t, "bravo")

	env.strandPrivateRegion(t, env.org.Slug, 5)
	env.strandPrivateRegion(t, bravo.Slug, 6)

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)

	bySubject := make(map[string]watchdog.Anomaly)

	for _, anomaly := range report.Anomalies {
		if anomaly.Detector == watchdog.DetectorDarkRegion {
			_, dup := bySubject[anomaly.Subject]
			r.False(dup, "duplicate dark-region subject %q", anomaly.Subject)
			bySubject[anomaly.Subject] = anomaly
		}
	}

	r.Len(bySubject, 2, "one anomaly per (org, @x), never merged")

	acme, ok := bySubject["acme/@x"]
	r.True(ok)
	r.Equal(5, acme.Count, "each org's anomaly counts only its own jobs")
	r.Equal(watchdog.SeverityCritical, acme.Severity)
	r.Contains(acme.Headline, `"@x" of org "acme"`)
	r.Contains(acme.Headline, "DARK")
	r.Contains(acme.Detail, "organization=acme")
	r.Contains(acme.Remediation, "/api/v1/orgs/acme/agents")
	r.NotContains(acme.Remediation, "/system/regions/migrate",
		"a private region is not fixed by migrating it to a cloud region")

	bravoAnomaly, ok := bySubject["bravo/@x"]
	r.True(ok)
	r.Equal(6, bravoAnomaly.Count)
	r.NotEqual(acme.Fingerprint(), bravoAnomaly.Fingerprint())

	r.Equal(11, report.StrandedJobs)
}

// TestDarkRegionDetectorPrivateRegionWithLiveAgentIsNotDark: an org agent that
// is connected serves its private region, so a backlog there is never reported
// as DARK. The positive control in the same run — the other org's `@x`, with
// no agent — must still be DARK, so the negative cannot pass by the detector
// ignoring private regions altogether.
func TestDarkRegionDetectorPrivateRegionWithLiveAgentIsNotDark(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	bravo := env.addOrg(t, "bravo")

	env.strandPrivateRegion(t, env.org.Slug, 5)
	env.insertLiveOrgAgent(t, env.org.UID, privateX)

	env.strandPrivateRegion(t, bravo.Slug, 5)

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)

	var served, dark *watchdog.Anomaly

	for i := range report.Anomalies {
		switch report.Anomalies[i].Subject {
		case "acme/@x":
			served = &report.Anomalies[i]
		case "bravo/@x":
			dark = &report.Anomalies[i]
		}
	}

	// A live agent with work three hours late is a stuck backlog — still worth
	// a warning, exactly as for a cloud region with live workers — but never
	// the critical "no live worker" page. Before the fix this same fixture
	// read as DARK/critical, because the agent was invisible to RegionHealth.
	r.NotNil(served)
	r.NotContains(served.Headline, "DARK", "a private region whose agent is live is not dark")
	r.Equal(watchdog.SeverityWarning, served.Severity)
	r.Contains(served.Detail, "liveWorkers=1")
	r.NotContains(served.Remediation, "/agents", "the agent-reconnect fix is only for a dark region")

	r.NotNil(dark, "positive control: the other org's agentless @x is dark")
	r.Contains(dark.Headline, "DARK")
	r.Equal(watchdog.SeverityCritical, dark.Severity)
	r.Contains(dark.Detail, "liveWorkers=0")
}

// TestDarkRegionDetectorPrivateRegionWithLiveAgentAndNoBacklog: the steady
// state of a healthy private location — agent connected, jobs not yet due —
// produces no dark-region anomaly at all.
func TestDarkRegionDetectorPrivateRegionWithLiveAgentAndNoBacklog(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	for range 6 {
		checkUID := env.createOrgCheck(t, env.org.Slug, []string{privateX})
		env.setJobScheduledAt(t, checkUID, time.Now().Add(time.Hour))
	}

	env.insertLiveOrgAgent(t, env.org.UID, privateX)

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)
	r.Nil(findAnomaly(report.Anomalies, watchdog.DetectorDarkRegion))
}
