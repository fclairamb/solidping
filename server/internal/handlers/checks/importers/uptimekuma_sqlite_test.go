package importers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
	"github.com/fclairamb/solidping/server/pkg/cli/kumadb"
)

// kumaSQLiteFixture is the real Uptime Kuma 2.5.4 database used by both
// server/pkg/cli/kumadb's own tests and this one — see the README next to it.
const kumaSQLiteFixture = "testdata/uptime-kuma-sqlite/kuma.db"

// TestUptimeKumaConverter_FromSQLite proves the two entry points cannot
// drift: kumadb.Read's reconstructed monitorList JSON, fed through the same
// UptimeKumaConverter the dashboard/API convert endpoint uses, must produce
// the same shape of ExportDocument a hand-authored 1.x backup JSON would.
// This is checked against its own golden snapshot (not the existing
// "uptime-kuma-backup" one — the fixtures describe different monitor sets).
func TestUptimeKumaConverter_FromSQLite(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backupJSON, err := kumadb.Read(context.Background(), kumaSQLiteFixture)
	r.NoError(err)

	converter := &importers.UptimeKumaConverter{}

	result, err := converter.Convert(backupJSON)
	r.NoError(err)

	assertGolden(t, "uptime-kuma-sqlite", result)
}
