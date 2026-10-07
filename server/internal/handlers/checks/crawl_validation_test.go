package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
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
		"period under one hour": {crawlReq("00:30:00", []string{"eu-west"}, nil), "period"},
		"private location":      {crawlReq("24:00:00", []string{"@office"}, nil), "cloud regions only"},
		"several regions":       {crawlReq("24:00:00", []string{"eu-west", "us-east"}, nil), "single region"},
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
}
