package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestCheckGroupUIDResolvedWithinOrg: checkGroupUid accepts the UID or the
// slug of a group of the caller's org, and refuses an unknown value or another
// org's group with ErrCheckGroupNotFound on create, update and clone.
func TestCheckGroupUIDResolvedWithinOrg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	other := models.NewOrganization("acme-other", "Acme Other")
	r.NoError(dbSvc.CreateOrganization(ctx, other))

	group := models.NewCheckGroup(org.UID, "Core", "core-services")
	r.NoError(dbSvc.CreateCheckGroup(ctx, group))
	foreign := models.NewCheckGroup(other.UID, "Foreign", "foreign")
	r.NoError(dbSvc.CreateCheckGroup(ctx, foreign))

	svc := NewService(dbSvc, notifier.NewLocalEventNotifier(), nil, nil)

	create := func(slug, groupRef string) (CheckResponse, error) {
		return svc.CreateCheck(ctx, org.Slug, CreateCheckRequest{
			Type: "http", Slug: slug, CheckGroupUID: &groupRef,
			Config: map[string]any{"url": "https://example.com"},
		})
	}

	// A slug resolves to the group's UID.
	created, err := create("acme-by-slug", "core-services")
	r.NoError(err)
	r.NotNil(created.CheckGroupUID)
	r.Equal(group.UID, *created.CheckGroupUID)

	// Unknown slug, unknown UUID, another org's group: refused.
	for _, ref := range []string{"no-such-group", "00000000-0000-4000-8000-000000000000", foreign.UID} {
		_, err = create("acme-bad", ref)
		r.ErrorIs(err, ErrCheckGroupNotFound, ref)
	}

	// Update: a foreign group is refused, "" removes, a slug resolves.
	foreignRef := foreign.UID
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &UpdateCheckRequest{CheckGroupUID: &foreignRef})
	r.ErrorIs(err, ErrCheckGroupNotFound)

	empty := ""
	updated, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &UpdateCheckRequest{CheckGroupUID: &empty})
	r.NoError(err)
	r.Nil(updated.CheckGroupUID)

	slugRef := "core-services"
	updated, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &UpdateCheckRequest{CheckGroupUID: &slugRef})
	r.NoError(err)
	r.NotNil(updated.CheckGroupUID)
	r.Equal(group.UID, *updated.CheckGroupUID)

	// Clone: a foreign group is refused, a slug resolves.
	_, err = svc.CloneCheck(ctx, org.Slug, created.UID, &CloneCheckRequest{CheckGroupUID: &foreignRef})
	r.ErrorIs(err, ErrCheckGroupNotFound)

	cloned, err := svc.CloneCheck(ctx, org.Slug, created.UID, &CloneCheckRequest{CheckGroupUID: &slugRef})
	r.NoError(err)
	r.NotNil(cloned.CheckGroupUID)
	r.Equal(group.UID, *cloned.CheckGroupUID)
}
