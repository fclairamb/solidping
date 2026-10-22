package checks_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

func crawlReq(period string, regions []string, cfg map[string]any) checks.CreateCheckRequest {
	if cfg == nil {
		cfg = map[string]any{"url": "https://www.acme.com/"}
	}

	return checks.CreateCheckRequest{Type: "crawl", Period: &period, Regions: regions, Config: cfg}
}

// Validation of the crawl check type (spec 2026-10-03-03).
func TestCreateCrawlCheckValidation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		req  checks.CreateCheckRequest
		want string
	}{
		"period under one hour":   {crawlReq("00:30:00", []string{"eu-west"}, nil), "period"},
		"several regions":         {crawlReq("24:00:00", []string{"eu-west", "us-east"}, nil), "single region"},
		"several private regions": {crawlReq("24:00:00", []string{"@dc1", "@dc2"}, nil), "single region"},
		"maxPages 0": {crawlReq("24:00:00", []string{"eu-west"},
			map[string]any{"url": "https://www.acme.com/", "maxPages": 0}), "maxPages"},
		"maxPages 2001": {crawlReq("24:00:00", []string{"eu-west"},
			map[string]any{"url": "https://www.acme.com/", "maxPages": 2001}), "maxPages"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)

			_, err := svc.CreateCheck(t.Context(), org.Slug, tc.req)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.want)
		})
	}

	t.Run("one cloud region, daily", func(t *testing.T) {
		t.Parallel()

		svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)

		created, err := svc.CreateCheck(t.Context(), org.Slug, crawlReq("24:00:00", []string{"eu-west"}, nil))
		require.NoError(t, err)
		require.Equal(t, []string{"eu-west"}, created.Regions)
	})
	// A crawl runs as a worker task an agent of the location claims (spec
	// 2026-10-22-03), so a private location is accepted, with no live agent.
	t.Run("one private location", func(t *testing.T) {
		t.Parallel()

		svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)

		created, err := svc.CreateCheck(t.Context(), org.Slug, crawlReq("24:00:00", []string{"@dc1"}, nil))
		require.NoError(t, err)
		require.Equal(t, []string{"@dc1"}, created.Regions)
	})
}

// recordingCanceler records the checks whose worker tasks were canceled.
type recordingCanceler struct {
	mu       sync.Mutex
	canceled []string
}

func (c *recordingCanceler) Cancel(_ context.Context, filter workertasks.CancelFilter) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.canceled = append(c.canceled, filter.CheckUID)

	return 1, nil
}

func (c *recordingCanceler) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.canceled...)
}

// Re-configuring, pausing or deleting a crawl cancels its run in progress
// (spec 2026-10-22-03). An edit that changes nothing a run sees does not.
func TestCrawlEditsCancelTheRunInProgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)
	canceler := &recordingCanceler{}
	svc.SetTaskCanceler(canceler)

	created, err := svc.CreateCheck(ctx, org.Slug, crawlReq("24:00:00", []string{"eu-west"}, nil))
	r.NoError(err)

	name := "renamed"
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)
	r.Empty(canceler.calls(), "a rename leaves the run alone")

	cfg := map[string]any{"url": "https://www.acme.com/blog/"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &cfg})
	r.NoError(err)
	r.Equal([]string{created.UID}, canceler.calls(), "a new config cancels the run")

	disabled := false
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Enabled: &disabled})
	r.NoError(err)
	r.Len(canceler.calls(), 2, "pausing cancels the run")

	r.NoError(svc.DeleteCheck(ctx, org.Slug, created.UID))
	r.Len(canceler.calls(), 3, "deleting cancels the run")
}
