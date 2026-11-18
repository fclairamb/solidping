package checks_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

// TestImportedTypedPasswordNeverReachesThePublicConfig follows an imported
// Chrome Recorder flow through ApplyChecks (spec 2026-11-18-02): the password
// it types lands in the encrypted private config, so neither the stored
// public config nor GET /checks/:uid (served the same to every role, viewers
// included) carries it.
func TestImportedTypedPasswordNeverReachesThePublicConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	svc, dbSvc, org := setupApplyService(t, true)
	ctx := t.Context()

	const password = "hunter2-acme"

	flow := []byte(`{"title": "Acme typed login", "steps": [
	  {"type": "navigate", "url": "https://app.acme.example/login"},
	  {"type": "change", "value": "ops@acme.example", "selectors": [["#email"]]},
	  {"type": "change", "value": "` + password + `", "selectors": [["#cred"], ["input[type=\"password\"]"]]}
	]}`)

	result, err := (&importers.ChromeRecorderConverter{}).Convert(flow)
	r.NoError(err)
	r.Len(result.Document.Checks, 1)

	document := result.Document
	document.Organization = org.Slug
	slug := document.Checks[0].Slug

	res, err := svc.ApplyChecks(ctx, org.Slug, document, checks.ApplyOptions{})
	r.NoError(err)
	r.Equal(1, res.Created)

	row, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, slug)
	r.NoError(err)

	public, err := json.Marshal(row.Config)
	r.NoError(err)
	r.NotContains(string(public), password, "the stored public config must not hold the password")
	r.Contains(string(public), "secrets.RECORDER_SECRET_1")
	r.NotNil(row.ConfigPrivate, "the password is kept, encrypted")

	got, err := svc.GetCheck(ctx, org.Slug, slug, checks.GetCheckOptions{})
	r.NoError(err)

	blob, err := json.Marshal(got)
	r.NoError(err)
	r.NotContains(string(blob), password, "GET /checks/:uid must not return the password")
	r.Contains(string(blob), "ops@acme.example", "positive control: the plain value is in the script")
}
