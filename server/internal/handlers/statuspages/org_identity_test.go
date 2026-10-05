package statuspages

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestPublicPayloadCarriesOrgIdentity(t *testing.T) {
	t.Parallel()

	logo := "/pub/assets/org-logo-uid"
	external := "https://cdn.acme.com/logo.png"

	testCases := []struct {
		name     string
		logoURL  *string
		wantLogo *string
	}{
		{name: "no logo is omitted", logoURL: nil, wantLogo: nil},
		{name: "uploaded logo passes through", logoURL: &logo, wantLogo: &logo},
		{name: "external logo is omitted (img-src 'self')", logoURL: &external, wantLogo: nil},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ctx, dbService, svc, org := whiteLabelSetup(t, config.DeploymentModeSelfHosted)
			createPublicPage(ctx, t, dbService, org.UID)

			if testCase.logoURL != nil {
				r.NoError(dbService.UpdateOrganization(ctx, org.UID, models.OrganizationUpdate{LogoURL: testCase.logoURL}))
			}

			public, err := svc.ViewStatusPage(ctx, "acme", testPublicSlug, AllViewOptions())
			r.NoError(err)
			r.Equal("Acme", public.OrgName)
			r.Equal(testCase.wantLogo, public.OrgLogoURL)
		})
	}
}
