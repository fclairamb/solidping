package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runSeverities(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	sev1 := models.NewSeverity(fix.org.UID, "sev-1", "Sev 1", []string{"email"}, true)
	sev2 := models.NewSeverity(fix.org.UID, "sev-2", "Sev 2", nil, false)
	must.NoError(svc.CreateSeverity(ctx, sev1))
	must.NoError(svc.CreateSeverity(ctx, sev2))

	bySlug, err := svc.GetSeverity(ctx, fix.org.UID, "sev-1")
	must.NoError(err)
	must.Equal(sev1.UID, bySlug.UID)

	list, err := svc.ListSeverities(ctx, &models.ListSeveritiesFilter{OrganizationUID: fix.org.UID})
	must.NoError(err)
	must.Len(list, 2)

	def, err := svc.GetOrgDefaultSeverity(ctx, fix.org.UID)
	must.NoError(err)
	must.Equal(sev1.UID, def.UID)

	// Promoting sev-2 first demotes the current default.
	must.NoError(svc.ClearOrgDefaultSeverity(ctx, fix.org.UID))
	isDefault := true
	name := "Sev Two"
	must.NoError(svc.UpdateSeverity(ctx, sev2.UID, &models.SeverityUpdate{Name: &name, IsDefault: &isDefault}))

	def, err = svc.GetOrgDefaultSeverity(ctx, fix.org.UID)
	must.NoError(err)
	must.Equal(sev2.UID, def.UID)
	must.Equal(name, def.Name)

	must.NoError(svc.DeleteSeverity(ctx, sev1.UID))
	_, err = svc.GetSeverity(ctx, fix.org.UID, "sev-1")
	must.Error(err, "a deleted severity is gone")
}
