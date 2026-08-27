package checks_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// validateEnv is a checks service on a real sqlite DB with a real entitlements
// service, so the rate projection reads the same rows the dispatch gate meters.
type validateEnv struct {
	t     *testing.T
	dbSvc *sqlite.Service
	svc   *checks.Service
	org   *models.Organization
}

// newValidateEnv builds the fixture. maxPerMinute <= 0 leaves the
// checks-per-minute cap unlimited.
func newValidateEnv(t *testing.T, maxPerMinute int) *validateEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("validate-org", "Validate Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	limits := entcore.Limits{}
	if maxPerMinute > 0 {
		limits.MaxChecksPerMinute = entcore.Int(maxPerMinute)
	}

	r.NoError(entSvc.Set(ctx, org.UID, entcore.Entitlements{
		Limits: limits, Source: models.EntitlementSourceAdmin,
	}, "user:test", ""))

	return &validateEnv{
		t:     t,
		dbSvc: dbSvc,
		org:   org,
		svc:   checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc),
	}
}

// httpCheck creates a real check through the service, the way a user would.
func (e *validateEnv) httpCheck(slug, period string, regions []string) checks.CheckResponse {
	e.t.Helper()

	resp, err := e.svc.CreateCheck(e.t.Context(), e.org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Slug:    slug,
		Config:  map[string]any{"url": "https://example.com"},
		Period:  &period,
		Regions: regions,
	})
	require.NoError(e.t, err)

	return resp
}

func (e *validateEnv) validate(req *checks.ValidateCheckRequest) checks.ValidateCheckResponse {
	e.t.Helper()

	resp, err := e.svc.ValidateCheck(e.t.Context(), e.org.Slug, req)
	require.NoError(e.t, err)

	return resp
}

// findingWithCode returns the finding carrying code, or nil.
func findingWithCode(fields []base.ValidationErrorField, code string) *base.ValidationErrorField {
	for i := range fields {
		if fields[i].Code == code {
			return &fields[i]
		}
	}

	return nil
}

// TestValidateReportsEveryFinding is the end of the first-error short-circuit:
// a payload that is wrong in three independent ways must come back describing
// all three, or the user pays a round trip per mistake.
func TestValidateReportsEveryFinding(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newValidateEnv(t, 0)

	resp := env.validate(&checks.ValidateCheckRequest{
		Type:   "http",
		Slug:   "Bad Slug!",
		Period: "00:00:01",
		Config: map[string]any{"url": "https://example.com", "timeout": "45s"},
	})

	r.False(resp.Valid)
	r.NotNil(findingWithCode(resp.Fields, checks.CodeInvalidSlug), "slug finding: %+v", resp.Fields)
	r.NotNil(findingWithCode(resp.Fields, checks.CodeInvalidPeriod), "period finding: %+v", resp.Fields)
	r.NotNil(findingWithCode(resp.Fields, checks.CodeInvalidConfig), "config finding: %+v", resp.Fields)

	// Positive control: the very same request minus the three mistakes is
	// clean, so the assertions above are about the mistakes and not about the
	// validator rejecting everything.
	clean := env.validate(&checks.ValidateCheckRequest{
		Type:   "http",
		Slug:   "good-slug",
		Period: "00:01:00",
		Config: map[string]any{"url": "https://example.com", "timeout": "10s"},
	})
	r.True(clean.Valid)
	r.Empty(clean.Fields)
}

// TestValidateEveryFindingCarriesSeverityAndCode pins the contract the
// frontend branches on.
func TestValidateEveryFindingCarriesSeverityAndCode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newValidateEnv(t, 1)
	env.httpCheck("taken", "00:01:00", nil)

	resp := env.validate(&checks.ValidateCheckRequest{
		Type:   "http",
		Slug:   "taken",
		Period: "00:00:10",
		Config: map[string]any{"url": "https://example.com"},
	})

	r.NotEmpty(resp.Fields)
	r.NotEmpty(resp.Warnings)

	for _, field := range resp.Fields {
		r.Equal(base.SeverityError, field.Severity, "field %q", field.Name)
		r.NotEmpty(field.Code, "field %q", field.Name)
		r.True(field.IsError())
	}

	for _, warning := range resp.Warnings {
		r.Equal(base.SeverityWarning, warning.Severity, "warning %q", warning.Name)
		r.NotEmpty(warning.Code, "warning %q", warning.Name)
		r.False(warning.IsError())
	}
}

// TestValidateWarningsAloneStayValid is the severity semantics: warnings never
// block. An org already over its cap must be able to keep editing.
func TestValidateWarningsAloneStayValid(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newValidateEnv(t, 1)

	resp := env.validate(&checks.ValidateCheckRequest{
		Type:   "http",
		Slug:   "fresh-check",
		Period: "00:00:10", // 6/min against a cap of 1
		Config: map[string]any{"url": "https://example.com"},
	})

	r.True(resp.Valid, "warnings must not invalidate: %+v", resp.Fields)
	r.Empty(resp.Fields)
	r.NotNil(findingWithCode(resp.Warnings, checks.CodeOrgRateOverLimit))
}

func TestValidateSlugUniqueness(t *testing.T) {
	t.Parallel()

	t.Run("a live sibling's slug collides", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 0)
		env.httpCheck("already-used", "00:01:00", nil)

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "already-used",
			Config: map[string]any{"url": "https://example.com"},
		})

		r.False(resp.Valid)
		r.NotNil(findingWithCode(resp.Fields, checks.CodeSlugTaken))
	})

	t.Run("excludeCheckUid stops a check flagging its own slug", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 0)
		existing := env.httpCheck("edited-check", "00:01:00", nil)

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "edited-check", ExcludeCheckUID: existing.UID,
			Config: map[string]any{"url": "https://example.com"},
		})

		r.True(resp.Valid, "own slug must not collide: %+v", resp.Fields)
		r.Nil(findingWithCode(resp.Fields, checks.CodeSlugTaken))
	})

	t.Run("a soft-deleted check releases its slug", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 0)
		env.httpCheck("recycled", "00:01:00", nil)
		r.NoError(env.svc.DeleteCheck(t.Context(), env.org.Slug, "recycled"))

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "recycled",
			Config: map[string]any{"url": "https://example.com"},
		})

		r.True(resp.Valid, "a deleted slug is free again: %+v", resp.Fields)
		r.Nil(findingWithCode(resp.Fields, checks.CodeSlugTaken))
	})

	t.Run("a malformed slug is reported as a format error, not a collision", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 0)

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "NOPE",
			Config: map[string]any{"url": "https://example.com"},
		})

		r.False(resp.Valid)
		r.NotNil(findingWithCode(resp.Fields, checks.CodeInvalidSlug))
		r.Nil(findingWithCode(resp.Fields, checks.CodeSlugTaken))
	})
}

func TestValidateOrgRateProjection(t *testing.T) {
	t.Parallel()

	t.Run("a create that pushes the org over the cap warns", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 2)
		env.httpCheck("existing", "00:00:30", nil) // 2/min — exactly at the cap

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "newcomer", Period: "00:01:00", // +1/min → 3/min
			Config: map[string]any{"url": "https://example.com"},
		})

		warning := findingWithCode(resp.Warnings, checks.CodeOrgRateOverLimit)
		r.NotNil(warning)
		r.Equal("period", warning.Name)
		r.Contains(warning.Message, "3")
		r.True(resp.Valid)
	})

	t.Run("a create that stays under the cap is silent", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 10)
		env.httpCheck("existing", "00:00:30", nil)

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "newcomer", Period: "00:01:00",
			Config: map[string]any{"url": "https://example.com"},
		})

		r.Nil(findingWithCode(resp.Warnings, checks.CodeOrgRateOverLimit))
	})

	t.Run("an edit that SHRINKS the rate clears the warning", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 2)
		existing := env.httpCheck("greedy", "00:00:10", nil) // 6/min, already over

		// Without the exclusion the stored 6/min would still be in the sum and
		// the warning would fire; with it, the proposal REPLACES the row.
		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "greedy", ExcludeCheckUID: existing.UID,
			Period: "00:01:00", // 1/min
			Config: map[string]any{"url": "https://example.com"},
		})

		r.Nil(findingWithCode(resp.Warnings, checks.CodeOrgRateOverLimit),
			"a repair edit must not be warned at: %+v", resp.Warnings)

		// Positive control: the same edit that does NOT shrink still warns.
		stillOver := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "greedy", ExcludeCheckUID: existing.UID,
			Period: "00:00:10",
			Config: map[string]any{"url": "https://example.com"},
		})
		r.NotNil(findingWithCode(stillOver.Warnings, checks.CodeOrgRateOverLimit))
	})

	t.Run("an edit that GROWS the rate past the cap warns", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 2)
		existing := env.httpCheck("modest", "00:01:00", nil) // 1/min

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "modest", ExcludeCheckUID: existing.UID,
			Period: "00:00:10", // 6/min
			Config: map[string]any{"url": "https://example.com"},
		})

		r.NotNil(findingWithCode(resp.Warnings, checks.CodeOrgRateOverLimit))
	})

	t.Run("regions multiply the projected rate", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 2)

		single := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "spread", Period: "00:01:00",
			Regions: []string{"default"},
			Config:  map[string]any{"url": "https://example.com"},
		})
		r.Nil(findingWithCode(single.Warnings, checks.CodeOrgRateOverLimit),
			"1 region × 1/min is under a cap of 2")

		multi := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "spread", Period: "00:01:00",
			Regions: []string{"default", "eu", "us"},
			Config:  map[string]any{"url": "https://example.com"},
		})
		r.NotNil(findingWithCode(multi.Warnings, checks.CodeOrgRateOverLimit),
			"3 regions × 1/min is over a cap of 2: %+v", multi.Warnings)
	})

	t.Run("a disabled check costs nothing", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 2)
		disabled := false

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "off", Period: "00:00:10", Enabled: &disabled,
			Config: map[string]any{"url": "https://example.com"},
		})

		r.Nil(findingWithCode(resp.Warnings, checks.CodeOrgRateOverLimit))
	})

	t.Run("passive types are exempt", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 1)

		// Positive control first: an ACTIVE check with this exact schedule warns.
		active := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "active-twin", Period: "00:01:00",
			Regions: []string{"default", "eu"},
			Config:  map[string]any{"url": "https://example.com"},
		})
		r.NotNil(findingWithCode(active.Warnings, checks.CodeOrgRateOverLimit))

		passive := env.validate(&checks.ValidateCheckRequest{
			Type: "heartbeat", Slug: "passive-twin", Period: "00:01:00",
			Regions: []string{"default", "eu"},
			Config:  map[string]any{},
		})
		r.Nil(findingWithCode(passive.Warnings, checks.CodeOrgRateOverLimit),
			"heartbeats draw no execution budget: %+v", passive.Warnings)
	})

	t.Run("an unlimited cap is never over", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 0)

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "unbounded", Period: "00:00:10",
			Config: map[string]any{"url": "https://example.com"},
		})

		r.Nil(findingWithCode(resp.Warnings, checks.CodeOrgRateOverLimit))
	})
}

// TestValidateAndWritePathShareTheValidator is the guard the spec asks for: a
// finding the dry run produces must also be enforced on the real path, and a
// config the dry run calls clean must actually save. If the two ever drift, the
// form starts lying in one direction or the other.
func TestValidateAndWritePathShareTheValidator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		config map[string]any
		period string
	}{
		{
			name:   "over-cap timeout",
			config: map[string]any{"url": "https://example.com", "timeout": "45s"},
			period: "00:01:00",
		},
		{
			name:   "unknown ipVersion",
			config: map[string]any{"url": "https://example.com", "ipVersion": "ipv9"},
			period: "00:01:00",
		},
		{
			name:   "dangling tunnel reference",
			config: map[string]any{"url": "https://example.com", "tunnelCheckUid": "no-such-check"},
			period: "00:01:00",
		},
		{
			name:   "period under the global floor",
			config: map[string]any{"url": "https://example.com"},
			period: "00:00:01",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			env := newValidateEnv(t, 0)
			ctx := t.Context()

			resp := env.validate(&checks.ValidateCheckRequest{
				Type: "http", Slug: "guarded", Period: testCase.period, Config: testCase.config,
			})
			r.False(resp.Valid, "validate must reject: %+v", resp)

			period := testCase.period
			_, err := env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
				Type: "http", Slug: "guarded", Period: &period, Config: testCase.config,
			})
			r.Error(err, "create must reject what validate rejected")
		})
	}

	t.Run("a config validate calls clean really saves", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		env := newValidateEnv(t, 0)
		ctx := t.Context()
		config := map[string]any{"url": "https://example.com", "timeout": "10s"}
		period := "00:01:00"

		resp := env.validate(&checks.ValidateCheckRequest{
			Type: "http", Slug: "clean-one", Period: period, Config: config,
		})
		r.True(resp.Valid, "%+v", resp.Fields)

		_, err := env.svc.CreateCheck(ctx, env.org.Slug, checks.CreateCheckRequest{
			Type: "http", Slug: "clean-one", Period: &period, Config: config,
		})
		r.NoError(err)
	})
}

// TestValidateAndPatchPathShareTheValidator does the same for the PATCH path,
// which validates a MERGED config rather than the submitted one.
func TestValidateAndPatchPathShareTheValidator(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newValidateEnv(t, 0)
	ctx := t.Context()
	env.httpCheck("patched", "00:01:00", nil)

	badConfig := map[string]any{"url": "https://example.com", "timeout": "45s"}

	resp := env.validate(&checks.ValidateCheckRequest{
		Type: "http", Slug: "patched", Config: badConfig,
	})
	r.False(resp.Valid)
	r.NotNil(findingWithCode(resp.Fields, checks.CodeInvalidConfig))

	_, err := env.svc.UpdateCheck(ctx, env.org.Slug, "patched", &checks.UpdateCheckRequest{
		Config: &badConfig,
	})
	r.Error(err, "PATCH must reject what validate rejected")
}

// TestValidateUnknownTypeStopsEarly: with no checker there is nothing else
// worth saying, and the answer must still be a well-formed finding.
func TestValidateUnknownTypeStopsEarly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newValidateEnv(t, 0)

	resp := env.validate(&checks.ValidateCheckRequest{Type: "not-a-type", Config: map[string]any{}})

	r.False(resp.Valid)
	r.Len(resp.Fields, 1)
	r.Equal(checks.CodeUnsupportedType, resp.Fields[0].Code)
	r.Equal(base.SeverityError, resp.Fields[0].Severity)
}

// TestProjectChecksPerMinuteReusesTheDemandFormula pins that the projection is
// the SAME arithmetic as the live figure, not a second implementation: with
// nothing proposed the projection's Current equals ChecksPerMinuteStatus.
func TestProjectChecksPerMinuteReusesTheDemandFormula(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newValidateEnv(t, 5)
	env.httpCheck("one", "00:00:30", nil)
	env.httpCheck("two", "00:01:00", nil)

	ctx := context.Background()

	entSvc := entcore.NewService(env.dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	live, err := entSvc.ChecksPerMinuteStatus(ctx, env.org.UID)
	r.NoError(err)

	projected, err := entSvc.ProjectChecksPerMinute(ctx, env.org.UID, entcore.CheckRateProposal{})
	r.NoError(err)

	r.InDelta(live.Demand, projected.Current, 0.0001)
	r.InDelta(live.Demand, projected.Demand, 0.0001)
	r.Equal(live.Limit, projected.Limit)

	// And a proposal is additive on top of it.
	withProposal, err := entSvc.ProjectChecksPerMinute(ctx, env.org.UID, entcore.CheckRateProposal{
		Type: "http", Period: time.Minute, Enabled: true,
	})
	r.NoError(err)
	r.InDelta(live.Demand+1, withProposal.Demand, 0.0001)
}
