package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

func TestStripCopySuffix(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"acme":           "acme",
		"acme-copy":      "acme",
		"acme-copy-2":    "acme",
		"acme-copy-copy": "acme-copy",
		"acme-copy-x":    "acme-copy-x",
		"-copy":          "-copy",
	}
	for in, want := range cases {
		if got := stripCopySuffix(in); got != want {
			t.Errorf("stripCopySuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripCopyNameSuffix(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Acme":          "Acme",
		"Acme (copy)":   "Acme",
		"Acme (copy) x": "Acme (copy) x",
	}
	for in, want := range cases {
		if got := stripCopyNameSuffix(in); got != want {
			t.Errorf("stripCopyNameSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCloneOfACloneIsNumbered(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("clone-slug-org", "Clone Slug Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := NewService(dbSvc, notifier.NewLocalEventNotifier(), nil, nil)
	slug := "acme-site"
	created, err := svc.CreateCheck(ctx, org.Slug, CreateCheckRequest{
		Type:   "http",
		Slug:   slug,
		Config: map[string]any{"url": "https://example.com"},
	})
	require.NoError(t, err)

	first, err := svc.CloneCheck(ctx, org.Slug, created.UID, &CloneCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, "acme-site-copy", *first.Slug)

	second, err := svc.CloneCheck(ctx, org.Slug, first.UID, &CloneCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, "acme-site-copy-2", *second.Slug)

	third, err := svc.CloneCheck(ctx, org.Slug, second.UID, &CloneCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, "acme-site-copy-3", *third.Slug)
}
