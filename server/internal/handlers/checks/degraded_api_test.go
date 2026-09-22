package checks_test

// The degraded-detection configuration over the API (spec 2026-09-22-03): the
// defaults a new check gets, the fact that 0 is WRITABLE for every rule knob
// (the whole reason those bun tags carry no `default:` clause), the M-of-N
// validation, and the `?wouldHaveFired=true` filter that carries the rollout.
//
// Plus the nullable-column contract: an unconfigured check stores NULL and the
// default is resolved at read time, so a caller that never mentions the five
// numerics — including one bypassing models.NewCheck entirely — runs the
// documented rules rather than five silently-disabled ones.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
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

	// And the API's resolved numbers came from NULL columns, not from five
	// values written at insert time. This is the whole point of the nullable
	// shape: nothing has to remember to write a default.
	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Nil(stored.DegradedFailures, "an unconfigured check stores NULL, not 5")
	r.Nil(stored.DegradedFailuresWindow)
	r.Nil(stored.DegradedSlow)
	r.Nil(stored.DegradedSlowWindow)
	r.Nil(stored.SlowThresholdMs)

	r.Equal(5, stored.EffectiveDegradedFailures())
	r.Equal(60, stored.EffectiveDegradedFailuresWindow())
	r.Equal(3, stored.EffectiveDegradedSlow())
	r.Equal(6, stored.EffectiveDegradedSlowWindow())
	r.Equal(0, stored.EffectiveSlowThresholdMs())
}

// TestRawInsertGetsDegradedDefaults is the gap the nullable columns close: an
// insert path that does NOT go through models.NewCheck — config-as-code, a
// future importer, any hand-built models.Check — used to store 0 for all five
// numerics and run with every rule silently off. Now it stores NULL and runs
// the documented defaults, both through the accessors and through the API.
func TestRawInsertGetsDegradedDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("degraded-raw", "Degraded Raw")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	name, slug := "bypass", "bypass"
	now := time.Now()
	// Deliberately NOT models.NewCheck: this is the bypassing caller.
	bare := &models.Check{
		UID:             uuid.New().String(),
		OrganizationUID: org.UID,
		Name:            &name,
		Slug:            &slug,
		Type:            "http",
		Config:          models.JSONMap{"url": "https://acme.com"},
		Enabled:         true,
		Period:          timeutils.Duration(time.Minute),
		Status:          models.CheckStatusCreated,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	r.NoError(dbSvc.CreateCheck(ctx, bare))

	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, bare.UID)
	r.NoError(err)
	r.Nil(stored.DegradedFailures)
	r.Equal(5, stored.EffectiveDegradedFailures(), "NULL resolves to the default, NOT to 0")
	r.Equal(60, stored.EffectiveDegradedFailuresWindow())
	r.Equal(3, stored.EffectiveDegradedSlow())
	r.Equal(6, stored.EffectiveDegradedSlowWindow())
	r.Equal(0, stored.EffectiveSlowThresholdMs())
	r.False(stored.DegradedEnabled,
		"degraded_enabled is NOT nullable, so a bypassing insert fails safe into the dry run")

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	response, err := svc.GetCheck(ctx, org.Slug, bare.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal(5, response.DegradedFailures, "the API answers with the resolved default, never 0")
	r.Equal(60, response.DegradedFailuresWindow)
	r.Equal(3, response.DegradedSlow)
	r.Equal(6, response.DegradedSlowWindow)
	r.Equal(0, response.SlowThresholdMs)
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
	r.NotNil(stored.DegradedFailures, "an explicit 0 must be stored, not collapsed into NULL")
	r.Equal(0, *stored.DegradedFailures)
	r.NotNil(stored.DegradedSlow)
	r.Equal(0, *stored.DegradedSlow)
	r.False(stored.DegradedEnabled)

	// And the accessors must not "helpfully" turn a stored 0 back into the
	// default — that would make the rules impossible to switch off.
	r.Equal(0, stored.EffectiveDegradedFailures())
	r.Equal(0, stored.EffectiveDegradedSlow())
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

// TestUpdateCheckDegradedPartialValidation is the half the original M <= N check
// missed: it only compared M and N when BOTH arrived in the same request, so a
// PATCH touching one of them stored a rule that can never fire. With the window
// now NULL on an unconfigured check, "compare against what the check will
// actually run under" is the only comparison that means anything.
func TestUpdateCheckDegradedPartialValidation(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("degraded-partial", "Degraded Partial")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "acme", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)

	// M alone, above the default window of 60.
	_, err = svc.UpdateCheck(ctx, org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedFailures: intPtr(70),
	})
	r.Error(err, "70 of the default 60 can never fire")

	// N alone, below the default M of 5.
	_, err = svc.UpdateCheck(ctx, org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedSlowWindow: intPtr(2),
	})
	r.Error(err, "shrinking the window under the default M of 3 strands the rule")

	// The legal shapes still pass: both together, and a positive control that
	// the defaults themselves are not somehow self-contradictory.
	_, err = svc.UpdateCheck(ctx, org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedFailures:       intPtr(70),
		DegradedFailuresWindow: intPtr(100),
	})
	r.NoError(err)

	// And after that edit the stored 70 is what the next partial PATCH is
	// measured against — not the code default any more.
	_, err = svc.UpdateCheck(ctx, org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedFailuresWindow: intPtr(50),
	})
	r.Error(err, "50 is below the stored M of 70")

	_, err = svc.UpdateCheck(ctx, org.Slug, check.UID, &checks.UpdateCheckRequest{
		DegradedFailuresWindow: intPtr(120),
	})
	r.NoError(err)
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
