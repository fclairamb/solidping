package entitlements_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
)

func TestCustomDomainAllowedUnlimitedWhenNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Self-hosted defaults leave MaxCustomDomains nil (unlimited).
	svc, org, _ := setup(t)
	r.NoError(svc.CustomDomainAllowed(t.Context(), org.UID))
}

func TestCustomDomainAllowedBlocksAtCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org, dbSvc := setup(t)

	r.NoError(svc.Set(ctx, org.UID, entitlements.Entitlements{
		Limits: entitlements.Limits{MaxCustomDomains: entitlements.Int(1)},
		Source: models.EntitlementSourceAdmin,
	}, "user:tester", ""))

	// No domains yet → allowed.
	r.NoError(svc.CustomDomainAllowed(ctx, org.UID))

	// Seed one page with a domain → at the cap.
	page := models.NewStatusPage(org.UID, "one", "one")
	d := "a.example.com"
	tok := "t"
	page.CustomDomain = &d
	page.CustomDomainToken = &tok
	r.NoError(dbSvc.CreateStatusPage(ctx, page))

	err := svc.CustomDomainAllowed(ctx, org.UID)
	r.ErrorIs(err, entitlements.ErrEntitlementExceeded)

	var quotaErr *entitlements.QuotaError
	r.ErrorAs(err, &quotaErr)
	r.Equal("MaxCustomDomains", quotaErr.LimitName)
	r.Equal(1, quotaErr.Limit)
	r.Equal(1, quotaErr.CurrentUsage)
}
