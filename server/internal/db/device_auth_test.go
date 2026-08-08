package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// testDeviceAuthRequests is the cross-engine (Postgres + SQLite) parity guard
// for the RFC 8628 device-authorization storage (spec 2026-08-08-02): the
// lookups only see live rows, resolution is a compare-and-set that a second
// responder loses, and consumption is the exactly-once gate behind PAT
// delivery. It runs under the shared testService harness so both dialects
// exercise the same assertions.
func testDeviceAuthRequests(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("device-auth-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	user := models.NewUser("device-auth@example.com")
	r.NoError(svc.CreateUser(ctx, user))

	// --- Create + lookups ---
	deviceCode := "dc-" + uuid.New().String()
	userCode := "ABCD2345"
	req := models.NewDeviceAuthRequest("sp CLI", deviceCode, userCode, time.Now().Add(15*time.Minute))
	r.NoError(svc.CreateDeviceAuthRequest(ctx, req))

	byUser, err := svc.GetDeviceAuthRequestByUserCode(ctx, userCode)
	r.NoError(err)
	r.Equal(req.UID, byUser.UID)
	r.Equal(models.DeviceAuthStatusPending, byUser.Status)
	r.Equal("sp CLI", byUser.ClientName)
	r.Nil(byUser.OrganizationUID, "a pending request is not bound to an org yet")

	byDevice, err := svc.GetDeviceAuthRequestByDeviceCode(ctx, deviceCode)
	r.NoError(err)
	r.Equal(req.UID, byDevice.UID)

	_, err = svc.GetDeviceAuthRequestByUserCode(ctx, "ZZZZ9999")
	r.Error(err, "an unknown user code must not resolve")

	// --- Poll stamp (slow_down input) ---
	polledAt := time.Now()
	r.NoError(svc.TouchDeviceAuthPoll(ctx, req.UID, polledAt))

	afterPoll, err := svc.GetDeviceAuthRequestByDeviceCode(ctx, deviceCode)
	r.NoError(err)
	r.NotNil(afterPoll.LastPolledAt)

	// --- Resolution is a compare-and-set ---
	res := &models.DeviceAuthResolution{
		Status:          models.DeviceAuthStatusApproved,
		UserUID:         user.UID,
		OrganizationUID: org.UID,
		TokenUID:        uuid.New().String(),
		TokenValue:      "pat_deadbeef",
	}
	won, err := svc.ResolveDeviceAuthRequest(ctx, req.UID, res)
	r.NoError(err)
	r.True(won, "first responder wins")

	wonAgain, err := svc.ResolveDeviceAuthRequest(ctx, req.UID, res)
	r.NoError(err)
	r.False(wonAgain, "a second decision on the same request must lose")

	approved, err := svc.GetDeviceAuthRequestByDeviceCode(ctx, deviceCode)
	r.NoError(err)
	r.Equal(models.DeviceAuthStatusApproved, approved.Status)
	r.NotNil(approved.OrganizationUID)
	r.Equal(org.UID, *approved.OrganizationUID, "the PAT is bound to the org chosen at approval")
	r.NotNil(approved.UserUID)
	r.Equal(user.UID, *approved.UserUID)
	r.NotNil(approved.TokenValue)
	r.Equal("pat_deadbeef", *approved.TokenValue)

	// --- Consumption is the exactly-once gate ---
	consumed, err := svc.ConsumeDeviceAuthRequest(ctx, req.UID)
	r.NoError(err)
	r.True(consumed, "the first consumer takes the token")

	consumedAgain, err := svc.ConsumeDeviceAuthRequest(ctx, req.UID)
	r.NoError(err)
	r.False(consumedAgain, "a second poll must not re-deliver the token")

	_, err = svc.GetDeviceAuthRequestByDeviceCode(ctx, deviceCode)
	r.Error(err, "a consumed request is gone")

	// --- Expiry: an expired row is invisible to both lookups, and purgeable ---
	expiredCode := "dc-expired-" + uuid.New().String()
	expired := models.NewDeviceAuthRequest("sp CLI", expiredCode, "EFGH6789", time.Now().Add(-time.Minute))
	r.NoError(svc.CreateDeviceAuthRequest(ctx, expired))

	_, err = svc.GetDeviceAuthRequestByDeviceCode(ctx, expiredCode)
	r.Error(err, "an expired request must not resolve by device code")

	_, err = svc.GetDeviceAuthRequestByUserCode(ctx, "EFGH6789")
	r.Error(err, "an expired request must not resolve by user code")

	purged, err := svc.PurgeExpiredDeviceAuthRequests(ctx, time.Now())
	r.NoError(err)
	r.GreaterOrEqual(purged, int64(1), "the expired row is swept")
}
