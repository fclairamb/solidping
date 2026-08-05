package checks_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestCreateCheckSlugMaxLengthPersistsToDB is an end-to-end check (through
// the real SQLite migration chain, not just the in-memory validateSlug
// regex) that a 100-char slug actually persists. The checks table's slug
// column previously carried a DB-level `length(slug) <= 50` CHECK constraint
// (migration 001) that drifted from, and would silently reject, the widened
// Go-level slugRegex — migration 009 rebuilds the table with `<= 100` to fix
// this (spec 2026-08-04-01).
func TestCreateCheckSlugMaxLengthPersistsToDB(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("slug-max-len", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)
	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)

	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), creds, entSvc)
	slug := "a" + strings.Repeat("b", 99)
	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "Long", Slug: slug, Type: "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)
	r.NotNil(created.Slug)
	r.Equal(slug, *created.Slug)
}
