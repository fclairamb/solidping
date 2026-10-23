package dbcontract

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

//nolint:funlen // linear contract scenario
func runCheckGroups(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	group := models.NewCheckGroup(fix.org.UID, "Production", "production")
	must.NoError(svc.CreateCheckGroup(ctx, group))

	got, err := svc.GetCheckGroup(ctx, fix.org.UID, group.UID)
	must.NoError(err)
	must.Equal("production", got.Slug)

	byIdent, err := svc.GetCheckGroupByUidOrSlug(ctx, fix.org.UID, "production")
	must.NoError(err)
	must.Equal(group.UID, byIdent.UID)

	byUID, err := svc.GetCheckGroupByUidOrSlug(ctx, fix.org.UID, group.UID)
	must.NoError(err)
	must.Equal(group.UID, byUID.UID)

	batch, err := svc.GetCheckGroupsByUIDs(ctx, fix.org.UID, []string{group.UID, uuid.New().String()})
	must.NoError(err)
	must.Len(batch, 1)
	must.Contains(batch, group.UID)

	groups, err := svc.ListCheckGroups(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(groups, 1)

	// A member check shows up in the group's check list and status counts.
	member := models.NewCheck(fix.org.UID, "grouped-check", "http")
	member.CheckGroupUID = &group.UID
	must.NoError(svc.CreateCheck(ctx, member))

	uids, err := svc.ListCheckUIDsByGroup(ctx, fix.org.UID, group.UID)
	must.NoError(err)
	must.Equal([]string{member.UID}, uids)

	counts, err := svc.GetCheckGroupStatusCounts(ctx, fix.org.UID)
	must.NoError(err)
	must.Contains(counts, group.UID)

	name := "Prod"
	order := int16(3)
	update := &models.CheckGroupUpdate{Name: &name, SortOrder: &order}
	must.NoError(svc.UpdateCheckGroup(ctx, fix.org.UID, group.UID, update))
	got, err = svc.GetCheckGroup(ctx, fix.org.UID, group.UID)
	must.NoError(err)
	must.Equal(name, got.Name)

	must.NoError(svc.DeleteCheckGroup(ctx, group.UID))
	_, err = svc.GetCheckGroup(ctx, fix.org.UID, group.UID)
	must.Error(err)
}
