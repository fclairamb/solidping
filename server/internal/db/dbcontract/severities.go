package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runSeverities(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	sev1 := models.NewSeverity(f.org.UID, "sev-1", "Sev 1", []string{"email"}, true)
	sev2 := models.NewSeverity(f.org.UID, "sev-2", "Sev 2", nil, false)
	r.NoError(svc.CreateSeverity(ctx, sev1))
	r.NoError(svc.CreateSeverity(ctx, sev2))

	bySlug, err := svc.GetSeverity(ctx, f.org.UID, "sev-1")
	r.NoError(err)
	r.Equal(sev1.UID, bySlug.UID)

	list, err := svc.ListSeverities(ctx, &models.ListSeveritiesFilter{OrganizationUID: f.org.UID})
	r.NoError(err)
	r.Len(list, 2)

	def, err := svc.GetOrgDefaultSeverity(ctx, f.org.UID)
	r.NoError(err)
	r.Equal(sev1.UID, def.UID)

	// Promoting sev-2 first demotes the current default.
	r.NoError(svc.ClearOrgDefaultSeverity(ctx, f.org.UID))
	isDefault := true
	name := "Sev Two"
	r.NoError(svc.UpdateSeverity(ctx, sev2.UID, &models.SeverityUpdate{Name: &name, IsDefault: &isDefault}))

	def, err = svc.GetOrgDefaultSeverity(ctx, f.org.UID)
	r.NoError(err)
	r.Equal(sev2.UID, def.UID)
	r.Equal(name, def.Name)

	r.NoError(svc.DeleteSeverity(ctx, sev1.UID))
	_, err = svc.GetSeverity(ctx, f.org.UID, "sev-1")
	r.Error(err, "a deleted severity is gone")
}
