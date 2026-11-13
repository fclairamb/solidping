package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestPatchingEmailAuthOptionsKeepsTheToken covers the dashboard's edit of an
// email check's sender-authentication options (spec 2026-11-12-03): the PATCH
// carries only the options, the inbound address token must survive it, and an
// empty config clears the options again.
func TestPatchingEmailAuthOptionsKeepsTheToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, _, org := setupPlaintextChecksService(t)

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "email-auth-patch",
		Slug:   "email-auth-patch",
		Type:   "email",
		Config: map[string]any{},
	})
	r.NoError(err)

	token, ok := created.Config["token"].(string)
	r.True(ok)
	r.NotEmpty(token)

	patch := map[string]any{
		"require_spf":      "pass",
		"require_dkim":     true,
		"require_dmarc":    "pass",
		"check_blocklists": true,
		"blocklists":       []any{"zen.spamhaus.org"},
	}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err)

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("pass", row.Config["require_spf"])
	r.Equal(true, row.Config["require_dkim"])
	r.Equal("pass", row.Config["require_dmarc"])
	r.Equal(true, row.Config["check_blocklists"])
	r.Equal([]any{"zen.spamhaus.org"}, row.Config["blocklists"])
	r.Equal(token, row.Config["token"], "the inbound token must survive a PATCH that never mentions it")

	empty := map[string]any{}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &empty})
	r.NoError(err)

	row, err = dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)

	for _, key := range []string{"require_spf", "require_dkim", "require_dmarc", "check_blocklists", "blocklists"} {
		r.NotContains(row.Config, key, "an empty config clears %s", key)
	}

	r.Equal(token, row.Config["token"])
}
