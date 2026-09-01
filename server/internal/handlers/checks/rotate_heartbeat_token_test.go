package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestRotateHeartbeatTokenInvalidatesOldTokenImmediately covers the spec's
// core guarantee: rotating a heartbeat check's token changes Config["token"],
// the OLD token is rejected by the ping endpoint right away — there is no
// grace period, unlike webhook signing-secret rotation, because heartbeat
// pings are frequent and the operator is expected to update the sender right
// after rotating — and the NEW token is accepted.
func TestRotateHeartbeatTokenInvalidatesOldTokenImmediately(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, dbSvc, _, org := setupPlaintextChecksService(t)

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "hb-rotate",
		Slug:   "hb-rotate",
		Type:   "heartbeat",
		Config: map[string]any{},
	})
	r.NoError(err)

	oldToken, ok := created.Config["token"].(string)
	r.True(ok, "check creation must auto-generate a token")
	r.NotEmpty(oldToken)

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	hbSvc := heartbeat.NewService(dbSvc, jobs, nil, nil, 0)

	// Old token works before rotation (positive control).
	r.NoError(hbSvc.ReceiveHeartbeat(ctx, org.Slug, created.UID, oldToken, "up", "", 0, "", "", "", nil))

	rotated, err := svc.RotateHeartbeatToken(ctx, org.Slug, created.UID)
	r.NoError(err)

	newToken, ok := rotated.Config["token"].(string)
	r.True(ok, "the rotated response must expose the new token")
	r.NotEmpty(newToken)
	r.NotEqual(oldToken, newToken)

	// Old token now rejected immediately — no grace period for heartbeat.
	err = hbSvc.ReceiveHeartbeat(ctx, org.Slug, created.UID, oldToken, "up", "", 0, "", "", "", nil)
	r.ErrorIs(err, heartbeat.ErrInvalidToken)

	// New token is accepted.
	r.NoError(hbSvc.ReceiveHeartbeat(ctx, org.Slug, created.UID, newToken, "up", "", 0, "", "", "", nil))

	// The rotation is durably persisted, not just reflected in the response.
	row, err := dbSvc.GetCheck(ctx, org.UID, created.UID)
	r.NoError(err)
	r.Equal(newToken, row.Config["token"])
}

// TestRotateHeartbeatTokenRejectsNonHeartbeatCheck ensures the endpoint is
// heartbeat-only: rotating any other check type is a 400-mapped error, not a
// silent no-op or a 500.
func TestRotateHeartbeatTokenRejectsNonHeartbeatCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, _, _, org := setupPlaintextChecksService(t)

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name:   "http-check",
		Slug:   "http-check",
		Type:   "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)

	_, err = svc.RotateHeartbeatToken(ctx, org.Slug, created.UID)
	r.ErrorIs(err, checks.ErrNotHeartbeatCheck)
}

// TestRotateHeartbeatTokenUnknownCheck ensures an unknown identifier 404s via
// the standard ErrCheckNotFound sentinel, same as GetCheck/UpdateCheck.
func TestRotateHeartbeatTokenUnknownCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	svc, _, _, org := setupPlaintextChecksService(t)

	_, err := svc.RotateHeartbeatToken(ctx, org.Slug, "does-not-exist")
	r.ErrorIs(err, checks.ErrCheckNotFound)
}
