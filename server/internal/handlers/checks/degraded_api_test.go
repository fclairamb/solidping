package checks_test

// The degraded-detection configuration over the API (spec 2026-09-22-03): the
// defaults a new check gets, the fact that 0 is WRITABLE for every rule knob
// (the whole reason those bun tags carry no `default:` clause), the M-of-N
// validation, and the `?wouldHaveFired=true` filter that carries the rollout.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func intPtr(value int) *int { return &value }

// TestCreateCheckDegradedDefaults pins the fleet-calibrated defaults and the
// rollout rule: a check created through the API is ON, while a check that
// predates the feature (the migration's column default) is off.
func TestCreateCheckDegradedDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("degraded-api", "Degraded API")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "acme",
		Type:   "http",
		Config: map[string]any{"url": "https://acme.com"},
	})
	r.NoError(err)

	r.Equal(5, created.DegradedFailures)
	r.Equal(60, created.DegradedFailuresWindow)
	r.Equal(3, created.DegradedSlow)
	r.Equal(6, created.DegradedSlowWindow)
	r.Equal(0, created.SlowThresholdMs, "the slow rule stays inert until an operator picks a threshold")
	r.True(created.DegradedEnabled, "on for a new check")
	r.Nil(created.DegradedWouldFireAt)
}

// TestCreateCheckDegradedZeroIsWritable is the regression this spec called out
// twice: with a `default:` on the bun tag, `degradedFailures: 0` would never
// reach the database and the rule could not be turned off at creation.
func TestCreateCheckDegradedZeroIsWritable(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("degraded-zero", "Degraded Zero")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	off := false

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:             "quiet",
		Type:             "http",
		Config:           map[string]any{"url": "https://acme.com"},
		DegradedFailures: intPtr(0),
		DegradedSlow:     intPtr(0),
		SlowThresholdMs:  intPtr(0),
		DegradedEnabled:  &off,
	})
	r.NoError(err)
	r.Equal(0, created.DegradedFailures)
	r.Equal(0, created.DegradedSlow)
	r.False(created.DegradedEnabled)

	// And it is the DATABASE that holds the zero, not just the response struct.
	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal(0, stored.DegradedFailures)
	r.Equal(0, stored.DegradedSlow)
	r.False(stored.DegradedEnabled)
}

// TestUpdateCheckDegradedValidation rejects a rule that could never fire.
func TestUpdateCheckDegradedValidation(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("degraded-valid", "Degraded Valid")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "acme", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	_, err = svc.UpdateCheck(ctx, org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedSlow:       intPtr(7),
		DegradedSlowWindow: intPtr(6),
	})
	r.Error(err, "7 of 6 can never fire, so it must not be accepted as an enabled rule")

	_, err = svc.UpdateCheck(ctx, org.Slug, check.UID, &checks.UpdateCheckRequest{
		SlowThresholdMs: intPtr(-1),
	})
	r.Error(err)

	// A legal edit goes through, and enabling retires the dry-run stamp.
	stamp := time.Now().Add(-time.Hour)
	r.NoError(dbSvc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{DegradedWouldFireAt: &stamp}))

	on := true
	updated, err := svc.UpdateCheck(ctx, org.Slug, check.UID, &checks.UpdateCheckRequest{
		SlowThresholdMs: intPtr(1200),
		DegradedEnabled: &on,
	})
	r.NoError(err)
	r.Equal(1200, updated.SlowThresholdMs)
	r.Nil(updated.DegradedWouldFireAt, "enabling the feature retires the would-have-fired banner")
}

// TestListChecksWouldHaveFiredFilter exercises the rollout filter through the
// handler, with a never-flagged check as the negative control.
func TestListChecksWouldHaveFiredFilter(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("would-have-fired", "Would Have Fired")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	flagged := models.NewCheck(org.UID, "wh-flagged", "http")
	flagged.DegradedEnabled = false
	r.NoError(dbSvc.CreateCheck(ctx, flagged))

	quiet := models.NewCheck(org.UID, "wh-quiet", "http")
	quiet.DegradedEnabled = false
	r.NoError(dbSvc.CreateCheck(ctx, quiet))

	stamp := time.Now().Add(-45 * time.Minute)
	r.NoError(dbSvc.UpdateCheck(ctx, flagged.UID, &models.CheckUpdate{DegradedWouldFireAt: &stamp}))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.GET("", handler.ListChecks)

	list := func(queryString string) []string {
		req := httptest.NewRequestWithContext(
			ctx, http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks"+queryString, http.NoBody)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		r.Equal(http.StatusOK, rec.Code)

		var body struct {
			Data []struct {
				Slug                string     `json:"slug"`
				DegradedWouldFireAt *time.Time `json:"degradedWouldFireAt"`
			} `json:"data"`
		}
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))

		slugs := make([]string, len(body.Data))
		for i, row := range body.Data {
			slugs[i] = row.Slug
		}

		return slugs
	}

	r.ElementsMatch([]string{"wh-flagged", "wh-quiet"}, list(""))
	r.Equal([]string{"wh-flagged"}, list("?wouldHaveFired=true"))
	// Anything other than "true" applies no filter, so a typo can never silently
	// hide the whole list.
	r.ElementsMatch([]string{"wh-flagged", "wh-quiet"}, list("?wouldHaveFired=yes"))
}
