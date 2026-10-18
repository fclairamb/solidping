package plugintypes_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/plugintypes"
)

const testSchema = `{
  "type": "object",
  "required": ["host"],
  "properties": {"host": {"type": "string"}, "token": {"type": "string"}},
  "x-solidping-secret-fields": ["token"]
}`

func reported(checkType, description string) protocol.TypeManifest {
	return protocol.TypeManifest{
		Type: checkType, Plugin: "acme", Version: "1.0.0", Protocol: "exec",
		Description: description, Labels: []string{"safe"}, DefaultPeriod: "5m",
		Schema: json.RawMessage(testSchema),
	}
}

func newStore(t *testing.T) (*plugintypes.Store, string, string) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	orgA := models.NewOrganization("orga", "Org A")
	r.NoError(dbSvc.CreateOrganization(ctx, orgA))

	orgB := models.NewOrganization("orgb", "Org B")
	r.NoError(dbSvc.CreateOrganization(ctx, orgB))

	return plugintypes.New(dbSvc.DB()), orgA.UID, orgB.UID
}

// A report is stored under the reporting org only: another org never sees
// it in a lookup or a listing (spec 2026-10-17-05 §18).
func TestReportIsScopedToTheOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	store, orgA, orgB := newStore(t)

	stored, err := store.Report(ctx, orgA, []protocol.TypeManifest{reported("x-acme", "first")})
	r.NoError(err)
	r.Equal(1, stored)

	remote, ok, err := store.Lookup(ctx, orgA, "x-acme")
	r.NoError(err)
	r.True(ok)
	r.Equal("first", remote.Meta().Description)
	r.Equal([]string{"token"}, remote.SecretFields())
	r.Contains(remote.Meta().Labels, "unsafe", "an exec type is always unsafe, whatever it declares")
	r.NotContains(remote.Meta().Labels, "safe")

	_, ok, err = store.Lookup(ctx, orgB, "x-acme")
	r.NoError(err)
	r.False(ok, "org B must not resolve org A's agent type")

	listB, err := store.List(ctx, orgB)
	r.NoError(err)
	r.Empty(listB)

	listA, err := store.List(ctx, orgA)
	r.NoError(err)
	r.Len(listA, 1)
}

// A later report of the same type replaces the earlier one.
func TestReportReplacesTheType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	store, orgA, _ := newStore(t)

	_, err := store.Report(ctx, orgA, []protocol.TypeManifest{reported("x-acme", "first")})
	r.NoError(err)

	_, err = store.Report(ctx, orgA, []protocol.TypeManifest{reported("x-acme", "second")})
	r.NoError(err)

	list, err := store.List(ctx, orgA)
	r.NoError(err)
	r.Len(list, 1)
	r.Equal("second", list[0].Meta().Description)
}

// A type that does not compile is skipped; the valid ones of the same report
// are still stored.
func TestReportSkipsInvalidTypes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	store, orgA, _ := newStore(t)

	noPrefix := reported("acme", "no prefix")
	brokenSchema := reported("x-broken", "broken")
	brokenSchema.Schema = json.RawMessage(`{"type": 42}`)
	badProtocol := reported("x-proto", "bad protocol")
	badProtocol.Protocol = "so"

	stored, err := store.Report(ctx, orgA, []protocol.TypeManifest{
		noPrefix, brokenSchema, badProtocol, reported("x-good", "good"),
	})
	r.NoError(err)
	r.Equal(1, stored)

	list, err := store.List(ctx, orgA)
	r.NoError(err)
	r.Len(list, 1)
	r.Equal("x-good", string(list[0].Meta().Type))

	// No org (a system agent) stores nothing.
	stored, err = store.Report(ctx, "", []protocol.TypeManifest{reported("x-good", "good")})
	r.NoError(err)
	r.Zero(stored)
}
