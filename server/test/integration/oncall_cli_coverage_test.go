package integration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/client"
)

// cliCovTestUserUID matches the user seeded by NewTestServer.
const cliCovTestUserUID = "10000000-0000-0000-0000-000000000002"

// TestCLICoverage_OnCallScheduleLifecycle exercises the on-call schedule CRUD,
// preview, override, and iCal-feed generated-client operations end to end
// against the real handlers.
func TestCLICoverage_OnCallScheduleLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)
	userUID := uuid.MustParse(cliCovTestUserUID)

	weekday := 1
	startAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)

	// Create a weekly schedule with the seeded user in the roster.
	createResp, err := apiClient.CreateOncallScheduleWithResponse(ctx, TestOrgSlug,
		client.CreateOncallScheduleJSONRequestBody{
			Name:           "CLI coverage schedule",
			Timezone:       "UTC",
			RotationType:   "weekly",
			HandoffTime:    "09:00",
			HandoffWeekday: &weekday,
			StartAt:        startAt,
			UserUids:       &[]uuid.UUID{userUID},
		})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	r.Equal("CLI coverage schedule", createResp.JSON201.Name)
	r.Equal("weekly", createResp.JSON201.RotationType)
	scheduleUID := createResp.JSON201.Uid

	// List — the new schedule must be present.
	listResp, err := apiClient.ListOncallSchedulesWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, listResp.StatusCode())
	r.NotNil(listResp.JSON200)
	r.NotNil(listResp.JSON200.Data)
	found := false
	for i := range *listResp.JSON200.Data {
		if (*listResp.JSON200.Data)[i].Uid == scheduleUID {
			found = true
		}
	}
	r.True(found, "created schedule must appear in the list")

	// Get.
	getResp, err := apiClient.GetOncallScheduleWithResponse(ctx, TestOrgSlug, scheduleUID)
	r.NoError(err)
	r.Equal(200, getResp.StatusCode())
	r.NotNil(getResp.JSON200)
	r.Equal(scheduleUID, getResp.JSON200.Uid)

	// Update the name.
	newName := "CLI coverage schedule renamed"
	updateResp, err := apiClient.UpdateOncallScheduleWithResponse(ctx, TestOrgSlug, scheduleUID,
		client.UpdateOncallScheduleJSONRequestBody{Name: &newName})
	r.NoError(err)
	r.Equal(200, updateResp.StatusCode())
	r.NotNil(updateResp.JSON200)
	r.Equal(newName, updateResp.JSON200.Name)

	// Preview the rotation — with one user in the roster it must yield a slot.
	previewResp, err := apiClient.PreviewOncallScheduleWithResponse(ctx, TestOrgSlug, scheduleUID,
		&client.PreviewOncallScheduleParams{})
	r.NoError(err)
	r.Equal(200, previewResp.StatusCode())
	r.NotNil(previewResp.JSON200)
	r.NotNil(previewResp.JSON200.Data)
	r.GreaterOrEqual(len(*previewResp.JSON200.Data), 1)
	r.Equal(userUID, (*previewResp.JSON200.Data)[0].UserUid)

	// Create an override.
	overrideStart := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	overrideEnd := overrideStart.Add(2 * time.Hour)
	reason := "cli coverage override"
	overrideResp, err := apiClient.CreateOncallOverrideWithResponse(ctx, TestOrgSlug, scheduleUID,
		client.CreateOncallOverrideJSONRequestBody{
			UserUid: userUID,
			StartAt: overrideStart,
			EndAt:   overrideEnd,
			Reason:  &reason,
		})
	r.NoError(err)
	r.Equal(201, overrideResp.StatusCode())
	r.NotNil(overrideResp.JSON201)
	r.Equal(userUID, overrideResp.JSON201.UserUID)
	r.Equal(scheduleUID, overrideResp.JSON201.ScheduleUID)
	overrideUID := overrideResp.JSON201.UID

	// List overrides — exactly the one we created.
	overridesResp, err := apiClient.ListOncallOverridesWithResponse(ctx, TestOrgSlug, scheduleUID,
		&client.ListOncallOverridesParams{})
	r.NoError(err)
	r.Equal(200, overridesResp.StatusCode())
	r.NotNil(overridesResp.JSON200)
	r.NotNil(overridesResp.JSON200.Data)
	r.Len(*overridesResp.JSON200.Data, 1)
	r.Equal(overrideUID, (*overridesResp.JSON200.Data)[0].UID)

	// Enable the iCal feed and fetch it via the raw public wrapper.
	enableResp, err := apiClient.EnableOncallIcalFeedWithResponse(ctx, TestOrgSlug, scheduleUID)
	r.NoError(err)
	r.Equal(200, enableResp.StatusCode())
	r.NotNil(enableResp.JSON200)
	r.NotEmpty(enableResp.JSON200.Secret)
	r.NotEmpty(enableResp.JSON200.Url)

	feed, err := apiClient.GetOnCallICalFeed(ctx, enableResp.JSON200.Secret)
	r.NoError(err)
	r.Contains(feed, "BEGIN:VCALENDAR", "feed must be a VCALENDAR document")

	// Rotate the feed secret — a fresh secret is returned.
	rotateResp, err := apiClient.RotateOncallIcalFeedWithResponse(ctx, TestOrgSlug, scheduleUID)
	r.NoError(err)
	r.Equal(200, rotateResp.StatusCode())
	r.NotNil(rotateResp.JSON200)
	r.NotEmpty(rotateResp.JSON200.Secret)

	// Disable the feed; the old secret must stop serving.
	disableResp, err := apiClient.DisableOncallIcalFeedWithResponse(ctx, TestOrgSlug, scheduleUID)
	r.NoError(err)
	r.Equal(204, disableResp.StatusCode())

	_, err = apiClient.GetOnCallICalFeed(ctx, rotateResp.JSON200.Secret)
	r.Error(err, "disabled feed secret must no longer serve")

	// Delete the override.
	delOverrideResp, err := apiClient.DeleteOncallOverrideWithResponse(ctx, TestOrgSlug, scheduleUID, overrideUID)
	r.NoError(err)
	r.Equal(204, delOverrideResp.StatusCode())

	// Delete the schedule; a subsequent GET must 404.
	deleteResp, err := apiClient.DeleteOncallScheduleWithResponse(ctx, TestOrgSlug, scheduleUID)
	r.NoError(err)
	r.Equal(204, deleteResp.StatusCode())

	goneResp, err := apiClient.GetOncallScheduleWithResponse(ctx, TestOrgSlug, scheduleUID)
	r.NoError(err)
	r.Equal(404, goneResp.StatusCode())
	r.Nil(goneResp.JSON200)
}
