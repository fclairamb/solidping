package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestPatchingHeartbeatConfigKeepsTheToken covers the trap the require_hmac
// option walks straight into (spec 2026-09-01-06).
//
// The public side of a config PATCH is REPLACE, not merge, and the heartbeat
// token is deliberately not a declared secret (the ping URL is built from it).
// Without the token-preservation rule, a PATCH of ANY other config key
// silently destroys the ping URL: every existing sender starts failing and
// nothing in the response says why.
func TestPatchingHeartbeatConfigKeepsTheToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, _, org := setupPlaintextChecksService(t)

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "hb-patch",
		Slug:   "hb-patch",
		Type:   "heartbeat",
		Config: map[string]any{},
	})
	r.NoError(err)

	token, ok := created.Config["token"].(string)
	r.True(ok)
	r.NotEmpty(token)

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	hbSvc := heartbeat.NewService(dbSvc, jobs, nil, nil, 0)

	// Positive control: the token works before the PATCH.
	r.NoError(hbSvc.ReceiveHeartbeat(ctx, org.Slug, created.UID, token, "up", "", 0, "", "", "", nil))

	patch := map[string]any{"require_hmac": true}
	updated, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err)

	r.Equal(true, updated.Config["require_hmac"], "the patched key must be applied")
	r.Equal(token, updated.Config["token"], "the ping token must survive a PATCH that never mentions it")

	// Durably, not just in the response.
	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal(token, row.Config["token"])

	// And the ping URL still works — the property that actually matters.
	r.NoError(hbSvc.ReceiveHeartbeat(ctx, org.Slug, created.UID, token, "up", "", 0, "", "", "", nil))
}

// TestPatchingHeartbeatTokenExplicitlyStillWins proves the preservation rule
// is "absent means unchanged", not "the token can never be written" — an
// import or a restore that carries an explicit token must still land.
func TestPatchingHeartbeatTokenExplicitlyStillWins(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, _, org := setupPlaintextChecksService(t)

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "hb-patch-explicit",
		Slug:   "hb-patch-explicit",
		Type:   "heartbeat",
		Config: map[string]any{},
	})
	r.NoError(err)

	original, ok := created.Config["token"].(string)
	r.True(ok)

	patch := map[string]any{"token": "an-explicitly-supplied-token"}
	updated, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &patch})
	r.NoError(err)

	r.Equal("an-explicitly-supplied-token", updated.Config["token"])
	r.NotEqual(original, updated.Config["token"])

	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal("an-explicitly-supplied-token", row.Config["token"])
}
