package baselinecapture_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/baselinecapture"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

const portBaselineCapturePG = 15641

//nolint:paralleltest // Shares one database across subtests
func TestCapture_SQLite(t *testing.T) {
	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: t.TempDir()})
	require.NoError(t, err)

	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	runCaptureSuite(t, svc)
}

//nolint:paralleltest // Shares one database across subtests
func TestCapture_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL test in short mode")
	}

	svc, err := postgres.NewEmbedded(t.Context(), "baseline-capture", portBaselineCapturePG, false, "", false, 0)
	if err != nil {
		testsupport.PostgresUnavailable(t, err)

		return
	}

	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.Initialize(t.Context()); err != nil {
		testsupport.PostgresInitFailed(t, err)

		return
	}

	runCaptureSuite(t, svc)
}

type fixture struct {
	org   *models.Organization
	check *models.Check
	jobs  []*models.CheckJob
}

func newDNSCheck(t *testing.T, svc db.Service, regions ...string) *fixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	org := models.NewOrganization("acme-"+uuid.NewString()[:8], "")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "dns-"+uuid.NewString()[:8], string(checkerdef.CheckTypeDNS))
	check.Config = models.JSONMap{"host": "acme.com", "record_type": "NS", "detect_changes": true}
	check.Regions = regions
	r.NoError(svc.CreateCheck(ctx, check))

	jobs, err := svc.ListCheckJobsByCheckUID(ctx, check.UID)
	r.NoError(err)
	r.NotEmpty(jobs)

	return &fixture{org: org, check: check, jobs: jobs}
}

func baselineOf(t *testing.T, config models.JSONMap) map[string]any {
	t.Helper()

	raw, ok := config["baseline"]
	if !ok || raw == nil {
		return map[string]any{}
	}

	baseline, ok := raw.(map[string]any)
	require.True(t, ok, "baseline must be an object, got %T", raw)

	return baseline
}

func reload(t *testing.T, svc db.Service, f *fixture) (*models.Check, []*models.CheckJob) {
	t.Helper()

	check, err := svc.GetCheck(t.Context(), f.org.UID, f.check.UID)
	require.NoError(t, err)

	jobs, err := svc.ListCheckJobsByCheckUID(t.Context(), f.check.UID)
	require.NoError(t, err)

	return check, jobs
}

func capture(ctx context.Context, svc db.Service, f *fixture, region string, values ...any) {
	baselinecapture.Capture(ctx, svc, f.jobs[0], f.check, &region,
		map[string]any{"baseline_capture": values})
}

func runCaptureSuite(t *testing.T, svc db.Service) {
	t.Helper()

	t.Run("first capture writes the check and its jobs", func(t *testing.T) {
		r := require.New(t)
		f := newDNSCheck(t, svc, "eu-west", "us-east")

		capture(t.Context(), svc, f, "eu-west", "ns1.acme.com", "ns2.acme.com")

		check, jobs := reload(t, svc, f)
		r.Equal([]any{"ns1.acme.com", "ns2.acme.com"}, baselineOf(t, check.Config)["eu-west"])
		r.Equal(true, check.Config["detect_changes"], "the rest of the config is untouched")
		r.Equal("acme.com", check.Config["host"])

		for _, job := range jobs {
			r.Equal([]any{"ns1.acme.com", "ns2.acme.com"}, baselineOf(t, job.Config)["eu-west"],
				"every job carries the materialized copy")
		}
	})

	t.Run("an existing region baseline is never overwritten", func(t *testing.T) {
		r := require.New(t)
		f := newDNSCheck(t, svc, "eu-west")

		capture(t.Context(), svc, f, "eu-west", "ns1.acme.com")
		capture(t.Context(), svc, f, "eu-west", "ns9.evil.com")

		check, _ := reload(t, svc, f)
		r.Equal([]any{"ns1.acme.com"}, baselineOf(t, check.Config)["eu-west"])
	})

	t.Run("two regions capturing concurrently both end up stored", func(t *testing.T) {
		r := require.New(t)

		regions := []string{"r1", "r2", "r3", "r4", "r5", "r6"}
		f := newDNSCheck(t, svc, regions...)

		var wg sync.WaitGroup

		for _, region := range regions {
			wg.Go(func() {
				capture(context.WithoutCancel(t.Context()), svc, f, region, "ns."+region+".acme.com")
			})
		}

		wg.Wait()

		check, jobs := reload(t, svc, f)
		baseline := baselineOf(t, check.Config)
		r.Len(baseline, len(regions))

		for _, region := range regions {
			r.Equal([]any{"ns." + region + ".acme.com"}, baseline[region], region)
		}

		for _, job := range jobs {
			r.Len(baselineOf(t, job.Config), len(regions), "jobs end on the latest config")
		}
	})

	t.Run("an audit event is recorded", func(t *testing.T) {
		r := require.New(t)
		f := newDNSCheck(t, svc, "eu-west")

		capture(t.Context(), svc, f, "eu-west", "ns1.acme.com", "ns2.acme.com")
		capture(t.Context(), svc, f, "eu-west", "ns3.acme.com") // no-op, no event

		events, err := svc.ListEvents(t.Context(), &models.ListEventsFilter{
			OrganizationUID: f.org.UID,
			CheckUID:        &f.check.UID,
			EventTypes:      []models.EventType{models.EventTypeCheckBaselineCaptured},
		})
		r.NoError(err)
		r.Len(events, 1)
		r.Equal(models.ActorTypeSystem, events[0].ActorType)
		r.Equal("eu-west", events[0].Payload[baselinecapture.EventPayloadRegion])
		r.EqualValues(2, events[0].Payload[baselinecapture.EventPayloadValueCount])
	})

	t.Run("no capture when detection was turned off meanwhile", func(t *testing.T) {
		r := require.New(t)
		f := newDNSCheck(t, svc, "eu-west")

		_, err := svc.DB().NewUpdate().Model((*models.Check)(nil)).
			Set("config = ?", models.JSONMap{"host": "acme.com"}).
			Where("uid = ?", f.check.UID).Exec(t.Context())
		r.NoError(err)

		capture(t.Context(), svc, f, "eu-west", "ns1.acme.com")

		check, _ := reload(t, svc, f)
		r.NotContains(check.Config, "baseline")
	})

	t.Run("non-dns jobs and empty captures are ignored", func(t *testing.T) {
		r := require.New(t)
		f := newDNSCheck(t, svc, "eu-west")

		capture(t.Context(), svc, f, "eu-west")

		other := *f.jobs[0]
		other.Type = string(checkerdef.CheckTypeHTTP)
		region := "eu-west"
		baselinecapture.Capture(t.Context(), svc, &other, nil, &region,
			map[string]any{"baseline_capture": []string{"x"}})

		check, _ := reload(t, svc, f)
		r.NotContains(check.Config, "baseline")
	})

	t.Run("a region-less run uses the default key", func(t *testing.T) {
		r := require.New(t)
		f := newDNSCheck(t, svc)

		baselinecapture.Capture(t.Context(), svc, f.jobs[0], f.check, nil,
			map[string]any{"baseline_capture": []string{"ns1.acme.com"}})

		check, _ := reload(t, svc, f)
		r.Equal([]any{"ns1.acme.com"}, baselineOf(t, check.Config)["default"])
	})

	t.Run("a hostile region key is refused", func(t *testing.T) {
		f := newDNSCheck(t, svc, "eu-west")

		written, err := svc.CaptureCheckConfigBaseline(t.Context(), f.check.UID, `a"b`, []string{"x"})
		require.ErrorIs(t, err, db.ErrInvalidBaselineKey)
		require.False(t, written)
		require.NotEmpty(t, fmt.Sprint(err))
	})
}
