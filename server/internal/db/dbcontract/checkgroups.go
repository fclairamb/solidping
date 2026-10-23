package dbcontract

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func runCheckGroups(t *testing.T, f *fixture) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)
	svc := f.svc

	group := models.NewCheckGroup(f.org.UID, "Production", "production")
	r.NoError(svc.CreateCheckGroup(ctx, group))

	got, err := svc.GetCheckGroup(ctx, f.org.UID, group.UID)
	r.NoError(err)
	r.Equal("production", got.Slug)

	byIdent, err := svc.GetCheckGroupByUidOrSlug(ctx, f.org.UID, "production")
	r.NoError(err)
	r.Equal(group.UID, byIdent.UID)

	byUID, err := svc.GetCheckGroupByUidOrSlug(ctx, f.org.UID, group.UID)
	r.NoError(err)
	r.Equal(group.UID, byUID.UID)

	batch, err := svc.GetCheckGroupsByUIDs(ctx, f.org.UID, []string{group.UID, uuid.New().String()})
	r.NoError(err)
	r.Len(batch, 1)
	r.Contains(batch, group.UID)

	groups, err := svc.ListCheckGroups(ctx, f.org.UID)
	r.NoError(err)
	r.Len(groups, 1)

	// A member check shows up in the group's check list and status counts.
	member := models.NewCheck(f.org.UID, "grouped-check", "http")
	member.CheckGroupUID = &group.UID
	r.NoError(svc.CreateCheck(ctx, member))

	uids, err := svc.ListCheckUIDsByGroup(ctx, f.org.UID, group.UID)
	r.NoError(err)
	r.Equal([]string{member.UID}, uids)

	counts, err := svc.GetCheckGroupStatusCounts(ctx, f.org.UID)
	r.NoError(err)
	r.Contains(counts, group.UID)

	name := "Prod"
	order := int16(3)
	r.NoError(svc.UpdateCheckGroup(ctx, f.org.UID, group.UID, &models.CheckGroupUpdate{Name: &name, SortOrder: &order}))
	got, err = svc.GetCheckGroup(ctx, f.org.UID, group.UID)
	r.NoError(err)
	r.Equal(name, got.Name)

	r.NoError(svc.DeleteCheckGroup(ctx, group.UID))
	_, err = svc.GetCheckGroup(ctx, f.org.UID, group.UID)
	r.Error(err)
}
