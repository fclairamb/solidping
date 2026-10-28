package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestSNMPCommunityIsSecret: the community string never reaches the public
// column, the API response or the export, and survives an unrelated edit.
func TestSNMPCommunityIsSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupEncryptedChecksServiceCreds(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "snmp", Slug: "snmp", Type: "snmp",
		Config: map[string]any{
			"host": "10.0.0.1", "oid": "1.3.6.1.2.1.1.3.0", "community": "s3cr3t-acme",
		},
	})
	r.NoError(err)
	r.NotContains(created.Config, "community")
	r.Contains(created.ConfigPrivateKeys, "community")

	got, err := svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotContains(got.Config, "community")
	r.Contains(got.ConfigPrivateKeys, "community")

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	rawCfg, err := json.Marshal(row.Config)
	r.NoError(err)
	r.NotContains(string(rawCfg), "s3cr3t-acme")
	r.Equal("s3cr3t-acme", decryptPrivate(t, creds, row)["community"])

	doc, err := svc.ExportChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	exported, err := json.Marshal(doc)
	r.NoError(err)
	r.NotContains(string(exported), "s3cr3t-acme")

	// Unrelated edit that does not resend the community keeps it.
	cfg := map[string]any{"host": "10.0.0.2", "oid": "1.3.6.1.2.1.1.3.0"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &cfg})
	r.NoError(err)

	after, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("s3cr3t-acme", decryptPrivate(t, creds, after)["community"])
}
