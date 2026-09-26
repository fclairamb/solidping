package auth

// Tests for the status-page embed allowlist exposed on the org settings
// endpoint (spec 2026-09-25-28): GET always returns an array, PATCH validates
// and normalizes, an empty array clears it, and one org's allowlist never
// leaks into another's.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/securityheaders"
)

func TestOrgSettingsEmbedOriginsRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("embed-a", "Embed A")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	other := models.NewOrganization("embed-b", "Embed B")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	got, err := svc.GetOrgSettings(ctx, org.Slug)
	r.NoError(err)
	r.NotNil(got.StatusPageAllowedEmbedOrigins, "always an array, never null")
	r.Empty(got.StatusPageAllowedEmbedOrigins)

	origins := []string{"https://Intranet.Acme.com/", "https://*.acme.com", "https://intranet.acme.com"}
	updated, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{
		StatusPageAllowedEmbedOrigins: &origins,
	})
	r.NoError(err)
	r.Equal([]string{"https://intranet.acme.com", "https://*.acme.com"}, updated.StatusPageAllowedEmbedOrigins)

	// Stored comma-separated under the documented key.
	param, err := dbSvc.GetOrgParameter(ctx, org.UID, models.ParamKeyStatusPageAllowedEmbedOrigins)
	r.NoError(err)
	r.NotNil(param)
	r.Equal("https://intranet.acme.com,https://*.acme.com", param.Value[models.ParameterValueKey])

	// Another org is unaffected.
	otherSettings, err := svc.GetOrgSettings(ctx, other.Slug)
	r.NoError(err)
	r.Empty(otherSettings.StatusPageAllowedEmbedOrigins)

	// Omitting the field leaves it alone.
	tracing := true
	untouched, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{TracerouteOnFailure: &tracing})
	r.NoError(err)
	r.Len(untouched.StatusPageAllowedEmbedOrigins, 2)

	// An empty array clears it (and deletes the row).
	empty := []string{}
	cleared, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{
		StatusPageAllowedEmbedOrigins: &empty,
	})
	r.NoError(err)
	r.Empty(cleared.StatusPageAllowedEmbedOrigins)

	param, err = dbSvc.GetOrgParameter(ctx, org.UID, models.ParamKeyStatusPageAllowedEmbedOrigins)
	r.NoError(err)
	r.Nil(param)
}

func TestOrgSettingsEmbedOriginsRejectsDirectiveInjection(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("embed-bad", "Embed Bad")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	for _, bad := range []string{
		"https://acme.com; script-src *",
		"*",
		"https://acme.com/status",
		"'unsafe-inline'",
		"https://acme.com https://evil.example",
	} {
		origins := []string{"https://ok.acme.com", bad}
		_, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{
			StatusPageAllowedEmbedOrigins: &origins,
		})
		r.ErrorIsf(err, securityheaders.ErrInvalidEmbedOrigin, "%q must be refused", bad)
	}

	// Nothing was stored by the refused writes.
	param, err := dbSvc.GetOrgParameter(ctx, org.UID, models.ParamKeyStatusPageAllowedEmbedOrigins)
	r.NoError(err)
	r.Nil(param)

	// A poisoned row written behind the API's back reads back filtered.
	r.NoError(dbSvc.SetOrgParameter(ctx, org.UID, models.ParamKeyStatusPageAllowedEmbedOrigins,
		"https://ok.acme.com,https://x.acme.com;script-src *", false))

	got, err := svc.GetOrgSettings(ctx, org.Slug)
	r.NoError(err)
	r.Equal([]string{"https://ok.acme.com"}, got.StatusPageAllowedEmbedOrigins)
}

// TestOrgSettingsEmbedOriginsRefusedBeforeAnyWrite pins that an invalid embed
// origin refuses the WHOLE update: a valid field sent alongside it must not be
// persisted by a request that answers with a validation error.
func TestOrgSettingsEmbedOriginsRefusedBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)

	org := models.NewOrganization("embed-atomic", "Embed Atomic")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	pattern := `^[^@]+@acme\.com$`
	tracing := false
	origins := []string{"https://acme.com/status"}

	_, err := svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{
		RegistrationEmailPattern:      &pattern,
		TracerouteOnFailure:           &tracing,
		StatusPageAllowedEmbedOrigins: &origins,
	})
	r.ErrorIs(err, securityheaders.ErrInvalidEmbedOrigin)

	got, err := svc.GetOrgSettings(ctx, org.Slug)
	r.NoError(err)
	r.Empty(got.RegistrationEmailPattern, "the pattern must not have been saved")
	r.True(got.TracerouteOnFailure, "the traceroute default must not have been saved")
	r.Empty(got.StatusPageAllowedEmbedOrigins)

	// Positive control: the same valid fields go through without the bad
	// origin, so the assertions above cannot pass vacuously.
	_, err = svc.UpdateOrgSettings(ctx, org.Slug, UpdateOrgSettingsRequest{
		RegistrationEmailPattern: &pattern,
		TracerouteOnFailure:      &tracing,
	})
	r.NoError(err)

	got, err = svc.GetOrgSettings(ctx, org.Slug)
	r.NoError(err)
	r.Equal(pattern, got.RegistrationEmailPattern)
	r.False(got.TracerouteOnFailure)
}
