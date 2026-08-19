package checks_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// browserWorker registers a live cloud worker whose reported capability set is
// given verbatim, so a test can express all three states: nil (never reported),
// a set without "browser" (a real no) and one with it (a yes).
func (e *warnEnv) browserWorker(slug, region string, caps []string) {
	e.t.Helper()

	r := require.New(e.t)
	ctx := e.t.Context()

	worker, err := e.dbSvc.RegisterOrUpdateWorker(ctx, &models.Worker{
		UID: uuid.New().String(), Slug: slug, Name: slug, Region: &region,
		Capabilities: caps,
	})
	r.NoError(err)

	_, err = e.dbSvc.DB().NewUpdate().Model((*models.Worker)(nil)).
		Set("last_active_at = ?", time.Now()).
		Where("uid = ?", worker.UID).Exec(ctx)
	r.NoError(err)
}

func browserCheckRequest(slug string, checkRegions []string) checks.CreateCheckRequest {
	return checks.CreateCheckRequest{
		Name:    "browser " + slug,
		Slug:    slug,
		Type:    "browser",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: checkRegions,
	}
}

// TestCreateBrowserCheckInChromelessRegionWarns is the proactive half of the
// spec: the write still goes through (advice, not a gate), and the warning
// names the region that cannot run it plus one that can.
func TestCreateBrowserCheckInChromelessRegionWarns(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{
		{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"},
		{Slug: "us", Emoji: "🇺🇸", Name: "United States"},
	})
	env.browserWorker("wrk-eu", "eu-west-1", []string{models.CapabilityIPv4})
	env.browserWorker("wrk-us", "us-east-1", []string{models.CapabilityIPv4, models.CapabilityBrowser})

	resp, err := env.svc.CreateCheck(ctx, env.org.Slug, browserCheckRequest("br-warn", []string{"eu"}))

	r.NoError(err, "an advertised 'no' must never reject the write")
	r.NotEmpty(resp.UID, "the check must actually be created")
	r.Len(resp.Warnings, 1)
	r.Contains(resp.Warnings[0].Message, "Europe", "the warning must name the region")
	r.Contains(resp.Warnings[0].Message, "United States", "and point at one that does have Chrome")
	r.Contains(resp.Warnings[0].Message, "headless Chrome")
}

// TestCreateBrowserCheckInUnknownRegionDoesNotWarn is the conflation guard: an
// agent predating the browser probe reports nothing, which is "unknown" and
// must never be presented as "this region has no Chrome". The second half is
// the positive control — flip the same worker to a reported set WITHOUT
// browser and the warning does appear, so the first assertion is about the null
// value and not about a warning that never fires at all.
func TestCreateBrowserCheckInUnknownRegionDoesNotWarn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"}})
	env.browserWorker("wrk-eu", "eu-west-1", nil) // live, but never reports

	resp, err := env.svc.CreateCheck(ctx, env.org.Slug, browserCheckRequest("br-unknown", []string{"eu"}))
	r.NoError(err)
	r.Empty(resp.Warnings, "'unknown' must never be reported as 'no'")

	env.browserWorker("wrk-eu", "eu-west-1", []string{models.CapabilityIPv4})

	resp, err = env.svc.CreateCheck(ctx, env.org.Slug, browserCheckRequest("br-known-no", []string{"eu"}))
	r.NoError(err)
	r.Len(resp.Warnings, 1)
}

// TestCreateBrowserCheckInCapableRegionDoesNotWarn is the other positive
// control: a region that advertises Chrome produces no warning at all.
func TestCreateBrowserCheckInCapableRegionDoesNotWarn(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"}})
	env.browserWorker("wrk-eu", "eu-west-1", []string{models.CapabilityIPv4, models.CapabilityBrowser})

	resp, err := env.svc.CreateCheck(ctx, env.org.Slug, browserCheckRequest("br-ok", []string{"eu"}))
	r.NoError(err)
	r.Empty(resp.Warnings)
}

// TestNonBrowserCheckNeverGetsBrowserWarning scopes the warning to the type: an
// HTTP check in a Chrome-less region is perfectly fine and must stay silent.
func TestNonBrowserCheckNeverGetsBrowserWarning(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"}})
	env.browserWorker("wrk-eu", "eu-west-1", []string{models.CapabilityIPv4})

	resp, err := env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
		Name:    "http check",
		Slug:    "http-check",
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: []string{"eu"},
	})
	r.NoError(err)
	r.Empty(resp.Warnings)
}

// TestValidateBrowserCheckWarnsBeforeSave is the live-form half: the config is
// valid, the form is not blocked, and the same advisory is attached — so the
// user sees it before hitting save rather than after the first failed run.
func TestValidateBrowserCheckWarnsBeforeSave(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)
	ctx := t.Context()

	env.setRegions([]regions.RegionDefinition{{Slug: "eu", Emoji: "🇪🇺", Name: "Europe"}})
	env.browserWorker("wrk-eu", "eu-west-1", []string{models.CapabilityIPv4})

	resp, err := env.svc.ValidateCheck(ctx, env.org.Slug, &checks.ValidateCheckRequest{
		Type:    "browser",
		Slug:    "br-validate",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: []string{"eu"},
	})

	r.NoError(err)
	r.True(resp.Valid, "the config is valid; the capability is only advice")
	r.Len(resp.Warnings, 1)
	r.Contains(resp.Warnings[0].Message, "headless Chrome")
}
