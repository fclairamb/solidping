package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// testService runs the full test suite against a db.Service implementation.
func testService(t *testing.T, svc db.Service) {
	t.Helper()

	ctx := t.Context()

	// Initialize the database
	err := svc.Initialize(ctx)
	require.NoError(t, err, "Initialize should not fail")

	t.Run("Organizations", func(t *testing.T) {
		testOrganizations(ctx, t, svc)
	})

	t.Run("Workers", func(t *testing.T) {
		testWorkers(ctx, t, svc)
	})

	t.Run("UsersWithOrg", func(t *testing.T) {
		testUsersWithOrg(ctx, t, svc)
	})

	t.Run("ChecksWithOrg", func(t *testing.T) {
		testChecksWithOrg(ctx, t, svc)
	})

	t.Run("ResultsWithCheckAndOrg", func(t *testing.T) {
		testResultsWithCheckAndOrg(ctx, t, svc)
	})

	t.Run("ResultStatusConstraint6_7_8", func(t *testing.T) {
		testResultStatusConstraint(ctx, t, svc)
	})

	t.Run("JSONMapHandling", func(t *testing.T) {
		testJSONMapHandling(ctx, t, svc)
	})

	t.Run("JobsWithOrg", func(t *testing.T) {
		testJobsWithOrg(ctx, t, svc)
	})

	t.Run("JobsWithoutOrg", func(t *testing.T) {
		testJobsWithoutOrg(ctx, t, svc)
	})

	t.Run("JobsCleanupRetention", func(t *testing.T) {
		testJobsCleanupRetention(ctx, t, svc)
	})

	t.Run("EventsKeysetPaginationTieBreak", func(t *testing.T) {
		testEventsKeysetPaginationTieBreak(ctx, t, svc)
	})

	t.Run("EventsTargetPayloadFilters", func(t *testing.T) {
		testEventsTargetPayloadFilters(ctx, t, svc)
	})

	t.Run("StateEntries", func(t *testing.T) {
		testStateEntries(ctx, t, svc)
	})

	t.Run("StatusPageSubscribers", func(t *testing.T) {
		testStatusPageSubscribers(ctx, t, svc)
	})

	t.Run("EscalationPolicyByUID", func(t *testing.T) {
		testEscalationPolicyByUID(ctx, t, svc)
	})

	t.Run("OnCallScheduleByUID", func(t *testing.T) {
		testOnCallScheduleByUID(ctx, t, svc)
	})

	t.Run("AppSettings", func(t *testing.T) {
		testAppSettings(ctx, t, svc)
	})

	t.Run("IncidentNotificationDeliveryDetails", func(t *testing.T) {
		testIncidentNotificationDeliveryDetails(ctx, t, svc)
	})

	t.Run("UpdateCheckStatusAndClocksTriState", func(t *testing.T) {
		testUpdateCheckStatusAndClocksTriState(ctx, t, svc)
	})

	t.Run("OAuthRepos", func(t *testing.T) {
		testOAuthRepos(ctx, t, svc)
	})

	t.Run("DeviceAuthRequests", func(t *testing.T) {
		testDeviceAuthRequests(ctx, t, svc)
	})

	t.Run("ChannelByPropertyForOrg", func(t *testing.T) {
		testChannelByPropertyForOrg(ctx, t, svc)
	})

	t.Run("ListChannelsByProperty", func(t *testing.T) {
		testListChannelsByProperty(ctx, t, svc)
	})

	t.Run("EmailSuppressions", func(t *testing.T) {
		testEmailSuppressions(ctx, t, svc)
	})

	t.Run("GetOrCreateSystemParameter", func(t *testing.T) {
		testGetOrCreateSystemParameter(ctx, t, svc)
	})

	t.Run("UserContactsByTypeValueOrdering", func(t *testing.T) {
		testUserContactsByTypeValueOrdering(ctx, t, svc)
	})
}

// testUserContactsByTypeValueOrdering is the cross-engine parity guard for the
// deterministic ordering of ListUserContactsByTypeValue.
//
// One Telegram chat can legitimately be linked in several organizations, and
// every caller that still has to pick a single row picks contacts[0]. Without an
// explicit ORDER BY that row is whatever the storage engine felt like returning
// first, so WHICH org answered /status could flip between two consecutive
// commands. Oldest link first, UID as the tiebreaker — on both engines.
func testUserContactsByTypeValueOrdering(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	chatID := "tg-order-" + suffix

	orgOld := models.NewOrganization("uco-old-"+suffix[len(suffix)-6:], "Old Org")
	r.NoError(svc.CreateOrganization(ctx, orgOld))

	orgNew := models.NewOrganization("uco-new-"+suffix[len(suffix)-6:], "New Org")
	r.NoError(svc.CreateOrganization(ctx, orgNew))

	userOld := models.NewUser("uco-old-" + suffix + "@example.com")
	r.NoError(svc.CreateUser(ctx, userOld))

	userNew := models.NewUser("uco-new-" + suffix + "@example.com")
	r.NoError(svc.CreateUser(ctx, userNew))

	now := time.Now()

	// Insert the NEWER link first: an unordered query would very plausibly hand
	// this one back first, which is exactly the bug being pinned.
	newer := models.NewUserContact(
		userNew.UID, orgNew.UID, models.UserContactTypeTelegram, chatID, "@new",
	)
	newer.CreatedAt = now
	r.NoError(svc.UpsertUserContact(ctx, newer))

	older := models.NewUserContact(
		userOld.UID, orgOld.UID, models.UserContactTypeTelegram, chatID, "@old",
	)
	older.CreatedAt = now.Add(-time.Hour)
	r.NoError(svc.UpsertUserContact(ctx, older))

	contacts, err := svc.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, chatID)
	r.NoError(err)
	r.Len(contacts, 2)
	r.Equal(orgOld.UID, contacts[0].OrganizationUID, "the oldest link must sort first")
	r.Equal(orgNew.UID, contacts[1].OrganizationUID)

	// And it is stable: repeating the query cannot reshuffle it.
	again, err := svc.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, chatID)
	r.NoError(err)
	r.Len(again, 2)
	r.Equal(contacts[0].UID, again[0].UID)
	r.Equal(contacts[1].UID, again[1].UID)
}

// testGetOrCreateSystemParameter is the cross-engine parity guard for the
// atomic insert-if-absent used to derive the Telegram webhook secret and bot
// username. The contract that matters operationally: the SECOND caller must be
// told it did not create the row and must be handed the FIRST caller's value —
// several API pods booting together have to converge on one secret, or the
// losers would validate inbound webhooks against a secret Telegram never got.
func testGetOrCreateSystemParameter(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	t.Run("CreatesThenAdopts", func(t *testing.T) {
		t.Parallel()

		key := "telegram.test_secret." + strconv.FormatInt(time.Now().UnixNano(), 10)

		first, created, err := svc.GetOrCreateSystemParameter(ctx, key, "first-value", true)
		r.NoError(err)
		r.True(created, "the first caller creates the row")
		r.NotNil(first)
		r.Equal("first-value", first.Value["value"])
		r.NotNil(first.Secret)
		r.True(*first.Secret, "the secret flag must be persisted")

		second, created, err := svc.GetOrCreateSystemParameter(ctx, key, "second-value", true)
		r.NoError(err)
		r.False(created, "the second caller must be told it did not create the row")
		r.NotNil(second)
		r.Equal(first.UID, second.UID, "same row")
		r.Equal("first-value", second.Value["value"],
			"the loser must adopt the winner's value, never keep its own")

		// And nothing was overwritten on disk either.
		stored, err := svc.GetSystemParameter(ctx, key)
		r.NoError(err)
		r.NotNil(stored)
		r.Equal("first-value", stored.Value["value"])
	})

	t.Run("Concurrent", func(t *testing.T) {
		t.Parallel()

		const callers = 8

		key := "telegram.test_concurrent." + strconv.FormatInt(time.Now().UnixNano(), 10)

		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			values  = make([]string, 0, callers)
			creates int
			errs    = make([]error, 0, callers)
		)

		wg.Add(callers)

		for i := range callers {
			go func(i int) {
				defer wg.Done()

				param, created, err := svc.GetOrCreateSystemParameter(
					ctx, key, "candidate-"+strconv.Itoa(i), true,
				)

				mu.Lock()
				defer mu.Unlock()

				if err != nil {
					errs = append(errs, err)

					return
				}

				if created {
					creates++
				}

				value, _ := param.Value["value"].(string)
				values = append(values, value)
			}(i)
		}

		wg.Wait()

		r.Empty(errs)
		r.Len(values, callers)
		r.Equal(1, creates, "exactly one caller may report created==true")

		for _, v := range values {
			r.Equal(values[0], v, "every concurrent caller must end up on the SAME value")
		}
	})
}

// testUpdateCheckStatusAndClocksTriState is the cross-engine parity guard for
// the merged status+clocks UPDATE (acceptance criterion 1): all three tri-state
// branches per clock column must behave identically on Postgres and SQLite —
// nil + !clear leaves the column untouched, nil + clear writes NULL, non-nil
// writes the value — and status/streak/status_changed_at land in the same call.
func testUpdateCheckStatusAndClocksTriState(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("tristate-clocks-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "tristate-clocks-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	reload := func() *models.Check {
		got, err := svc.GetCheck(ctx, org.UID, check.UID)
		r.NoError(err)

		return got
	}

	// Seed both clocks with known non-nil values via the merged UPDATE.
	seedFail := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	seedSucc := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Second)
	r.NoError(svc.UpdateCheckStatusAndClocks(
		ctx, check.UID, models.CheckStatusDown, 3, nil,
		models.IncidentClockUpdate{FirstFailureAt: &seedFail, FirstSuccessSinceFailureAt: &seedSucc},
	))

	c := reload()
	r.Equal(models.CheckStatusDown, c.Status, "non-nil status written")
	r.Equal(3, c.StatusStreak, "streak written")
	r.NotNil(c.FirstFailureAt)
	r.NotNil(c.FirstSuccessSinceFailureAt)
	r.WithinDuration(seedFail, c.FirstFailureAt.UTC(), time.Second, "non-nil clock value written")
	r.WithinDuration(seedSucc, c.FirstSuccessSinceFailureAt.UTC(), time.Second)

	// Branch (a) nil + !clear → both clock columns must stay untouched.
	r.NoError(svc.UpdateCheckStatusAndClocks(
		ctx, check.UID, models.CheckStatusValidating, 4, nil,
		models.IncidentClockUpdate{},
	))
	c = reload()
	r.Equal(models.CheckStatusValidating, c.Status)
	r.Equal(4, c.StatusStreak)
	r.NotNil(c.FirstFailureAt, "nil + !clear must leave first_failure_at untouched")
	r.NotNil(c.FirstSuccessSinceFailureAt, "nil + !clear must leave first_success_since_failure_at untouched")
	r.WithinDuration(seedFail, c.FirstFailureAt.UTC(), time.Second)
	r.WithinDuration(seedSucc, c.FirstSuccessSinceFailureAt.UTC(), time.Second)

	// Branch (b) nil + clear → both clock columns must become NULL.
	r.NoError(svc.UpdateCheckStatusAndClocks(
		ctx, check.UID, models.CheckStatusUp, 1, nil,
		models.IncidentClockUpdate{ClearFirstFailureAt: true, ClearFirstSuccessSinceFailureAt: true},
	))
	c = reload()
	r.Nil(c.FirstFailureAt, "nil + clear must write NULL to first_failure_at")
	r.Nil(c.FirstSuccessSinceFailureAt, "nil + clear must write NULL to first_success_since_failure_at")

	// Branch (c) non-nil again on a now-NULL column → value written, plus
	// status_changed_at lands in the same call and the untouched column stays NULL.
	newFail := time.Now().Add(-1 * time.Minute).UTC().Truncate(time.Second)
	changedAt := time.Now().UTC().Truncate(time.Second)
	r.NoError(svc.UpdateCheckStatusAndClocks(
		ctx, check.UID, models.CheckStatusDown, 2, &changedAt,
		models.IncidentClockUpdate{FirstFailureAt: &newFail},
	))
	c = reload()
	r.NotNil(c.FirstFailureAt, "non-nil must write the value")
	r.WithinDuration(newFail, c.FirstFailureAt.UTC(), time.Second)
	r.Nil(c.FirstSuccessSinceFailureAt, "untouched-on-this-call column stays NULL")
	r.NotNil(c.StatusChangedAt, "non-nil statusChangedAt written")
	r.WithinDuration(changedAt, c.StatusChangedAt.UTC(), time.Second)
}

// testIncidentNotificationDeliveryDetails is the cross-engine parity guard for
// the delivery_details column (acceptance criterion 4): a failed delivery
// persists structured artifacts that round-trip identically on Postgres and
// SQLite, and a row without details reads back as nil — no crash, no empty box.
func testIncidentNotificationDeliveryDetails(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("dd-notif-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "dd-notif-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "outage")
	r.NoError(svc.CreateIncident(ctx, incident))

	// Row WITH delivery details, written via the by-job failure path.
	jobUID := uuid.New().String()
	withDetails := models.NewIncidentNotificationForJob(
		org.UID, incident.UID, "incident.created",
		models.IncidentNotificationSourceCheckConnection,
		"conn-1", jobUID, "webhook", nil, nil,
	)
	// No real integration row exists in this harness; clear the FK so the
	// connection_uid -> integrations(uid) constraint is not exercised.
	withDetails.ConnectionUID = nil
	r.NoError(svc.CreateIncidentNotification(ctx, withDetails))

	details := &models.DeliveryDetails{
		HTTPStatusCode: 503,
		RequestURL:     "https://hooks.example.com/path",
		RequestBody:    `{"type":"incident.created"}`,
		ResponseBody:   `{"error":"unavailable"}`,
		DurationMs:     42,
		ResponseHeaders: map[string]string{
			"Retry-After": "120",
		},
	}
	r.NoError(svc.MarkIncidentNotificationFailedByJob(
		ctx, jobUID, time.Now(), "webhook request failed: status 503", false, details,
	))

	got, err := svc.GetIncidentNotification(ctx, org.UID, incident.UID, withDetails.UID)
	r.NoError(err)
	r.NotNil(got.DeliveryDetails, "delivery details must persist and read back")
	r.Equal(503, got.DeliveryDetails.HTTPStatusCode)
	r.Equal("https://hooks.example.com/path", got.DeliveryDetails.RequestURL)
	r.Contains(got.DeliveryDetails.RequestBody, "incident.created")
	r.Contains(got.DeliveryDetails.ResponseBody, "unavailable")
	r.Equal(int64(42), got.DeliveryDetails.DurationMs)
	r.Equal("120", got.DeliveryDetails.ResponseHeaders["Retry-After"])
	r.Equal(models.IncidentNotificationStatusFailed, got.Status)

	// Row WITHOUT delivery details reads back as nil (pre-feature / unsupported
	// channel), proving the column is genuinely nullable on both engines.
	jobUID2 := uuid.New().String()
	noDetails := models.NewIncidentNotificationForJob(
		org.UID, incident.UID, "incident.created",
		models.IncidentNotificationSourceCheckConnection,
		"conn-1", jobUID2, "email", nil, nil,
	)
	noDetails.ConnectionUID = nil
	r.NoError(svc.CreateIncidentNotification(ctx, noDetails))
	r.NoError(svc.MarkIncidentNotificationSentByJob(ctx, jobUID2, time.Now(), "msg-1", nil))

	gotNil, err := svc.GetIncidentNotification(ctx, org.UID, incident.UID, noDetails.UID)
	r.NoError(err)
	r.Nil(gotNil.DeliveryDetails, "a row written without details must read back nil")
	r.Equal(models.IncidentNotificationStatusSent, gotNil.Status)
}

func testEscalationPolicyByUID(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	org := models.NewOrganization("esc-by-uid", "")
	require.NoError(t, svc.CreateOrganization(ctx, org))

	policy := models.NewEscalationPolicy(org.UID, "Primary")
	require.NoError(t, svc.CreateEscalationPolicy(ctx, policy))

	byUID, err := svc.GetEscalationPolicy(ctx, org.UID, policy.UID)
	require.NoError(t, err)
	require.Equal(t, policy.UID, byUID.UID)

	// A slug-shaped (non-UUID) identifier resolves to no rows, not a 500.
	_, err = svc.GetEscalationPolicy(ctx, org.UID, "primary")
	require.Error(t, err)

	_, err = svc.GetEscalationPolicy(ctx, org.UID, "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
}

func testOnCallScheduleByUID(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	org := models.NewOrganization("oncall-by-uid", "")
	require.NoError(t, svc.CreateOrganization(ctx, org))

	schedule := models.NewOnCallSchedule(org.UID, "Primary", "UTC", models.RotationTypeDaily)
	require.NoError(t, svc.CreateOnCallSchedule(ctx, schedule))

	byUID, err := svc.GetOnCallSchedule(ctx, org.UID, schedule.UID)
	require.NoError(t, err)
	require.Equal(t, schedule.UID, byUID.UID)

	// A slug-shaped (non-UUID) identifier resolves to no rows, not a 500.
	_, err = svc.GetOnCallSchedule(ctx, org.UID, "primary")
	require.Error(t, err)

	_, err = svc.GetOnCallSchedule(ctx, org.UID, "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
}

func testOrganizations(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	t.Run("CreateAndGet", func(t *testing.T) {
		org := models.NewOrganization("test-org", "")

		err := svc.CreateOrganization(ctx, org)
		require.NoError(t, err, "CreateOrganization should not fail")

		retrieved, err := svc.GetOrganization(ctx, org.UID)
		require.NoError(t, err, "GetOrganization should not fail")
		assert.Equal(t, org.UID, retrieved.UID)
		assert.Equal(t, org.Slug, retrieved.Slug)
	})

	t.Run("GetBySlug", func(t *testing.T) {
		org := models.NewOrganization("slug-test-org", "")
		err := svc.CreateOrganization(ctx, org)
		require.NoError(t, err)

		retrieved, err := svc.GetOrganizationBySlug(ctx, "slug-test-org")
		require.NoError(t, err)
		assert.Equal(t, org.UID, retrieved.UID)
	})

	t.Run("List", func(t *testing.T) {
		org1 := models.NewOrganization("list-org-1", "")
		org2 := models.NewOrganization("list-org-2", "")

		err := svc.CreateOrganization(ctx, org1)
		require.NoError(t, err)
		err = svc.CreateOrganization(ctx, org2)
		require.NoError(t, err)

		orgs, err := svc.ListOrganizations(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(orgs), 2)

		found1, found2 := false, false

		for _, o := range orgs {
			if o.UID == org1.UID {
				found1 = true
			}

			if o.UID == org2.UID {
				found2 = true
			}
		}

		assert.True(t, found1, "org1 should be in the list")
		assert.True(t, found2, "org2 should be in the list")
	})

	t.Run("Update", func(t *testing.T) {
		org := models.NewOrganization("update-org", "")
		err := svc.CreateOrganization(ctx, org)
		require.NoError(t, err)

		newSlug := "updated-slug"
		err = svc.UpdateOrganization(ctx, org.UID, models.OrganizationUpdate{Slug: &newSlug})
		require.NoError(t, err)

		updated, err := svc.GetOrganization(ctx, org.UID)
		require.NoError(t, err)
		assert.Equal(t, newSlug, updated.Slug)
	})

	t.Run("CreateWithName", func(t *testing.T) {
		org := models.NewOrganization("named-org", "Named Organization")
		err := svc.CreateOrganization(ctx, org)
		require.NoError(t, err)

		retrieved, err := svc.GetOrganization(ctx, org.UID)
		require.NoError(t, err)
		assert.Equal(t, "Named Organization", retrieved.Name)
		assert.Equal(t, "named-org", retrieved.Slug)
	})

	t.Run("UpdateName", func(t *testing.T) {
		org := models.NewOrganization("update-name-org", "Original Name")
		err := svc.CreateOrganization(ctx, org)
		require.NoError(t, err)

		newName := "Updated Org Name"
		err = svc.UpdateOrganization(ctx, org.UID, models.OrganizationUpdate{Name: &newName})
		require.NoError(t, err)

		updated, err := svc.GetOrganization(ctx, org.UID)
		require.NoError(t, err)
		assert.Equal(t, "Updated Org Name", updated.Name)
	})

	t.Run("Delete", func(t *testing.T) {
		org := models.NewOrganization("delete-org", "")
		err := svc.CreateOrganization(ctx, org)
		require.NoError(t, err)

		err = svc.DeleteOrganization(ctx, org.UID)
		require.NoError(t, err)

		_, err = svc.GetOrganization(ctx, org.UID)
		assert.Error(t, err, "GetOrganization should fail for deleted org")
	})

	testOrganizationsGetNonExistent(ctx, t, svc)
}

func testOrganizationsGetNonExistent(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	t.Run("GetNonExistent", func(t *testing.T) {
		_, err := svc.GetOrganization(ctx, "non-existent-uid")
		assert.Error(t, err)
	})
}

func testWorkers(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	t.Run("CreateAndGet", func(t *testing.T) {
		worker := models.NewWorker("worker-1", "Worker 1")

		err := svc.CreateWorker(ctx, worker)
		require.NoError(t, err)

		retrieved, err := svc.GetWorker(ctx, worker.UID)
		require.NoError(t, err)
		assert.Equal(t, worker.UID, retrieved.UID)
		assert.Equal(t, worker.Slug, retrieved.Slug)
		assert.Equal(t, worker.Name, retrieved.Name)
	})

	t.Run("GetBySlug", func(t *testing.T) {
		worker := models.NewWorker("worker-slug", "Worker Slug Test")
		err := svc.CreateWorker(ctx, worker)
		require.NoError(t, err)

		retrieved, err := svc.GetWorkerBySlug(ctx, "worker-slug")
		require.NoError(t, err)
		assert.Equal(t, worker.UID, retrieved.UID)
	})

	t.Run("List", func(t *testing.T) {
		worker1 := models.NewWorker("list-worker-1", "List Worker 1")
		worker2 := models.NewWorker("list-worker-2", "List Worker 2")

		err := svc.CreateWorker(ctx, worker1)
		require.NoError(t, err)
		err = svc.CreateWorker(ctx, worker2)
		require.NoError(t, err)

		workers, err := svc.ListWorkers(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(workers), 2)
	})

	t.Run("UpdateWithContext", func(t *testing.T) {
		worker := models.NewWorker("ctx-worker", "Context Worker")
		err := svc.CreateWorker(ctx, worker)
		require.NoError(t, err)

		newName := "Updated Worker"
		newRegion := "eu-west-1"
		now := time.Now()
		err = svc.UpdateWorker(ctx, worker.UID, models.WorkerUpdate{
			Name:         &newName,
			Region:       &newRegion,
			LastActiveAt: &now,
		})
		require.NoError(t, err)

		updated, err := svc.GetWorker(ctx, worker.UID)
		require.NoError(t, err)
		assert.Equal(t, newName, updated.Name)
		assert.Equal(t, "eu-west-1", *updated.Region)
		assert.NotNil(t, updated.LastActiveAt)
	})

	t.Run("Delete", func(t *testing.T) {
		worker := models.NewWorker("del-worker", "Delete Worker")
		err := svc.CreateWorker(ctx, worker)
		require.NoError(t, err)

		err = svc.DeleteWorker(ctx, worker.UID)
		require.NoError(t, err)

		_, err = svc.GetWorker(ctx, worker.UID)
		assert.Error(t, err)
	})
}

func testUsersWithOrg(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	// Create an organization first
	org := models.NewOrganization("user-test-org", "")
	err := svc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	t.Run("CreateAndGet", func(t *testing.T) {
		user := models.NewUser("user@example.com")

		err := svc.CreateUser(ctx, user)
		require.NoError(t, err)

		retrieved, err := svc.GetUser(ctx, user.UID)
		require.NoError(t, err)
		assert.Equal(t, user.UID, retrieved.UID)
		assert.Equal(t, user.Email, retrieved.Email)
	})

	t.Run("GetByEmail", func(t *testing.T) {
		user := models.NewUser("lookup@example.com")
		err := svc.CreateUser(ctx, user)
		require.NoError(t, err)

		retrieved, err := svc.GetUserByEmail(ctx, "lookup@example.com")
		require.NoError(t, err)
		assert.Equal(t, user.UID, retrieved.UID)
	})

	t.Run("List", func(t *testing.T) {
		user1 := models.NewUser("list1@example.com")
		user2 := models.NewUser("list2@example.com")

		err := svc.CreateUser(ctx, user1)
		require.NoError(t, err)
		err = svc.CreateUser(ctx, user2)
		require.NoError(t, err)

		users, err := svc.ListUsers(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(users), 2)
	})

	t.Run("Update", func(t *testing.T) {
		user := models.NewUser("update@example.com")
		err := svc.CreateUser(ctx, user)
		require.NoError(t, err)

		newName := "Updated Name"
		newPasswordHash := "$2a$10$somehash"
		err = svc.UpdateUser(ctx, user.UID, &models.UserUpdate{
			Name:         &newName,
			PasswordHash: &newPasswordHash,
		})
		require.NoError(t, err)

		updated, err := svc.GetUser(ctx, user.UID)
		require.NoError(t, err)
		assert.Equal(t, newName, updated.Name)
		assert.Equal(t, &newPasswordHash, updated.PasswordHash)
	})

	t.Run("Delete", func(t *testing.T) {
		user := models.NewUser("delete@example.com")
		err := svc.CreateUser(ctx, user)
		require.NoError(t, err)

		err = svc.DeleteUser(ctx, user.UID)
		require.NoError(t, err)

		_, err = svc.GetUser(ctx, user.UID)
		assert.Error(t, err)
	})

	t.Run("OrganizationMember", func(t *testing.T) {
		user := models.NewUser("member@example.com")
		err := svc.CreateUser(ctx, user)
		require.NoError(t, err)

		member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
		err = svc.CreateOrganizationMember(ctx, member)
		require.NoError(t, err)

		retrieved, err := svc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
		require.NoError(t, err)
		assert.Equal(t, member.UID, retrieved.UID)
		assert.Equal(t, models.MemberRoleAdmin, retrieved.Role)

		members, err := svc.ListMembersByOrg(ctx, org.UID)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(members), 1)

		newRole := models.MemberRoleViewer
		err = svc.UpdateOrganizationMember(ctx, member.UID, models.OrganizationMemberUpdate{Role: &newRole})
		require.NoError(t, err)

		updated, err := svc.GetOrganizationMember(ctx, member.UID)
		require.NoError(t, err)
		assert.Equal(t, models.MemberRoleViewer, updated.Role)
	})

	t.Run("ListMembersByUserWithOrgName", func(t *testing.T) {
		namedOrg := models.NewOrganization("member-named-org", "Member Named Org")
		err := svc.CreateOrganization(ctx, namedOrg)
		require.NoError(t, err)

		user := models.NewUser("member-named@example.com")
		err = svc.CreateUser(ctx, user)
		require.NoError(t, err)

		member := models.NewOrganizationMember(namedOrg.UID, user.UID, models.MemberRoleAdmin)
		err = svc.CreateOrganizationMember(ctx, member)
		require.NoError(t, err)

		members, err := svc.ListMembersByUser(ctx, user.UID)
		require.NoError(t, err)
		require.Len(t, members, 1)
		require.NotNil(t, members[0].Organization)
		assert.Equal(t, "Member Named Org", members[0].Organization.Name)
		assert.Equal(t, "member-named-org", members[0].Organization.Slug)
	})
}

func testChecksWithOrg(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	// Create an organization first
	org := models.NewOrganization("check-test-org", "")
	err := svc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	t.Run("CreateAndGet", func(t *testing.T) {
		check := models.NewCheck(org.UID, "http-check", "http")
		checkName := "HTTP Check"
		check.Name = &checkName
		check.Config = models.JSONMap{
			"url":     "https://example.com",
			"timeout": 30,
		}

		err := svc.CreateCheck(ctx, check)
		require.NoError(t, err)

		retrieved, err := svc.GetCheck(ctx, org.UID, check.UID)
		require.NoError(t, err)
		assert.Equal(t, check.UID, retrieved.UID)
		assert.Equal(t, check.Slug, retrieved.Slug)
		assert.Equal(t, check.Type, retrieved.Type)
		assert.Equal(t, "https://example.com", retrieved.Config["url"])
	})

	t.Run("GetByUidOrSlug_WithSlug", func(t *testing.T) {
		check := models.NewCheck(org.UID, "slug-check", "ping")
		err := svc.CreateCheck(ctx, check)
		require.NoError(t, err)

		// Test lookup by slug
		retrieved, err := svc.GetCheckByUidOrSlug(ctx, org.UID, "slug-check")
		require.NoError(t, err)
		assert.Equal(t, check.UID, retrieved.UID)

		// Test lookup by UID
		retrievedByUID, err := svc.GetCheckByUidOrSlug(ctx, org.UID, check.UID)
		require.NoError(t, err)
		assert.Equal(t, check.UID, retrievedByUID.UID)
	})

	t.Run("GetByEmailToken", func(t *testing.T) {
		token := "feedfacefeedfacefeedfacefeedfacefeedfacefeedface"
		check := models.NewCheck(org.UID, "email-check", "email")
		check.Config = models.JSONMap{"token": token}
		err := svc.CreateCheck(ctx, check)
		require.NoError(t, err)

		retrieved, err := svc.GetCheckByEmailToken(ctx, token)
		require.NoError(t, err)
		assert.Equal(t, check.UID, retrieved.UID)

		// Unknown token should return an error (sql.ErrNoRows)
		_, err = svc.GetCheckByEmailToken(ctx, "unknown-token")
		require.Error(t, err)
	})

	t.Run("List", func(t *testing.T) {
		// Create checks with different periods to validate correct storage and retrieval
		check1 := models.NewCheck(org.UID, "list-check-1", "http")
		check1.Period = timeutils.Duration(time.Second) // 1 second

		check2 := models.NewCheck(org.UID, "list-check-2", "tcp")
		check2.Period = timeutils.Duration(time.Minute) // 1 minute

		check3 := models.NewCheck(org.UID, "list-check-3", "ping")
		check3.Period = timeutils.Duration(90 * time.Minute) // 90 minutes

		err := svc.CreateCheck(ctx, check1)
		require.NoError(t, err)
		err = svc.CreateCheck(ctx, check2)
		require.NoError(t, err)
		err = svc.CreateCheck(ctx, check3)
		require.NoError(t, err)

		checks, _, err := svc.ListChecks(ctx, org.UID, nil)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(checks), 3)

		// Build a map of checks by UID for easy lookup
		checkMap := make(map[string]*models.Check)
		for i := range checks {
			checkMap[checks[i].UID] = checks[i]
		}

		// Validate that all three checks exist with correct periods
		retrieved1, found1 := checkMap[check1.UID]
		assert.True(t, found1, "check1 should be in the list")
		if found1 {
			assert.Equal(t, time.Second, time.Duration(retrieved1.Period), "check1 period should be 1 second")
			assert.Equal(t, check1.Slug, retrieved1.Slug)
			assert.Equal(t, check1.Type, retrieved1.Type)
		}

		retrieved2, found2 := checkMap[check2.UID]
		assert.True(t, found2, "check2 should be in the list")
		if found2 {
			assert.Equal(t, time.Minute, time.Duration(retrieved2.Period), "check2 period should be 1 minute")
			assert.Equal(t, check2.Slug, retrieved2.Slug)
			assert.Equal(t, check2.Type, retrieved2.Type)
		}

		retrieved3, found3 := checkMap[check3.UID]
		assert.True(t, found3, "check3 should be in the list")
		if found3 {
			assert.Equal(t, 90*time.Minute, time.Duration(retrieved3.Period), "check3 period should be 90 minutes")
			assert.Equal(t, check3.Slug, retrieved3.Slug)
			assert.Equal(t, check3.Type, retrieved3.Type)
		}
	})

	t.Run("Update", func(t *testing.T) {
		check := models.NewCheck(org.UID, "update-check", "http")
		err := svc.CreateCheck(ctx, check)
		require.NoError(t, err)

		newName := "Updated Check"
		newEnabled := false
		newConfig := models.JSONMap{"url": "https://updated.com", "timeout": 60}
		err = svc.UpdateCheck(ctx, check.UID, &models.CheckUpdate{
			Name:    &newName,
			Enabled: &newEnabled,
			Config:  &newConfig,
		})
		require.NoError(t, err)

		updated, err := svc.GetCheck(ctx, org.UID, check.UID)
		require.NoError(t, err)
		assert.Equal(t, newName, *updated.Name)
		assert.False(t, updated.Enabled)
		assert.Equal(t, "https://updated.com", updated.Config["url"])
	})

	t.Run("ListWithPagination", func(t *testing.T) {
		// Create a separate org to isolate from other tests
		paginationOrg := models.NewOrganization("pagination-test-org", "")
		err := svc.CreateOrganization(ctx, paginationOrg)
		require.NoError(t, err)

		// Create 5 checks with distinct timestamps
		createdChecks := make([]*models.Check, 5)
		for i := range 5 {
			name := fmt.Sprintf("paginate-check-%d", i)
			check := models.NewCheck(paginationOrg.UID, name, "http")
			checkName := fmt.Sprintf("Paginate Check %d", i)
			check.Name = &checkName
			errCreate := svc.CreateCheck(ctx, check)
			require.NoError(t, errCreate)
			createdChecks[i] = check
			time.Sleep(10 * time.Millisecond) // ensure distinct created_at
		}

		// Page 1: limit 2
		page1, total, err := svc.ListChecks(ctx, paginationOrg.UID, &models.ListChecksFilter{Limit: 2})
		require.NoError(t, err)
		assert.Equal(t, int64(5), total)
		// We request limit+1 internally but the DB returns limit+1; the service trims.
		// At the DB level we get limit+1 = 3 results.
		assert.Len(t, page1, 3, "DB should return limit+1 results when there are more")

		// Page 2: use cursor from last item of page 1 (index 1, since we'd take first 2)
		cursor := page1[1] // second item (the service would use this as cursor)
		page2, total2, err := svc.ListChecks(ctx, paginationOrg.UID, &models.ListChecksFilter{
			Limit:           2,
			CursorCreatedAt: &cursor.CreatedAt,
			CursorUID:       &cursor.UID,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(5), total2)
		assert.GreaterOrEqual(t, len(page2), 2, "Should have at least 2 more results")

		// Verify no overlap between pages
		page1UIDs := map[string]bool{page1[0].UID: true, page1[1].UID: true}
		for _, check := range page2 {
			assert.False(t, page1UIDs[check.UID], "Page 2 should not contain items from page 1")
		}
	})

	t.Run("ListWithSearch", func(t *testing.T) {
		// Create a separate org
		searchOrg := models.NewOrganization("search-test-org", "")
		err := svc.CreateOrganization(ctx, searchOrg)
		require.NoError(t, err)

		// Create checks with different names and slugs
		alpha := models.NewCheck(searchOrg.UID, "alpha-api", "http")
		alphaName := "Alpha API Monitor"
		alpha.Name = &alphaName

		beta := models.NewCheck(searchOrg.UID, "beta-web", "http")
		betaName := "Beta Website"
		beta.Name = &betaName

		gamma := models.NewCheck(searchOrg.UID, "gamma-api", "tcp")
		gammaName := "Gamma Service"
		gamma.Name = &gammaName

		require.NoError(t, svc.CreateCheck(ctx, alpha))
		require.NoError(t, svc.CreateCheck(ctx, beta))
		require.NoError(t, svc.CreateCheck(ctx, gamma))

		// Search by slug substring
		results, total, err := svc.ListChecks(ctx, searchOrg.UID, &models.ListChecksFilter{Query: "api"})
		require.NoError(t, err)
		assert.Equal(t, int64(2), total, "Should match alpha-api and gamma-api by slug")
		assert.Len(t, results, 2)

		// Search by name substring (case-insensitive)
		results, total, err = svc.ListChecks(ctx, searchOrg.UID, &models.ListChecksFilter{Query: "BETA"})
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		assert.Len(t, results, 1)
		assert.Equal(t, beta.UID, results[0].UID)

		// Search with no matches
		results, total, err = svc.ListChecks(ctx, searchOrg.UID, &models.ListChecksFilter{Query: "nonexistent"})
		require.NoError(t, err)
		assert.Equal(t, int64(0), total)
		assert.Empty(t, results)
	})

	t.Run("ListWithSearchAndPagination", func(t *testing.T) {
		// Create a separate org
		comboOrg := models.NewOrganization("combo-test-org", "")
		err := svc.CreateOrganization(ctx, comboOrg)
		require.NoError(t, err)

		// Create 4 checks matching "http" and 1 that doesn't
		for i := range 4 {
			slug := fmt.Sprintf("http-check-%d", i)
			check := models.NewCheck(comboOrg.UID, slug, "http")
			name := fmt.Sprintf("HTTP Check %d", i)
			check.Name = &name
			require.NoError(t, svc.CreateCheck(ctx, check))
			time.Sleep(10 * time.Millisecond)
		}
		other := models.NewCheck(comboOrg.UID, "dns-check", "dns")
		otherName := "DNS Check"
		other.Name = &otherName
		require.NoError(t, svc.CreateCheck(ctx, other))

		// Search "http" with limit 2
		results, total, err := svc.ListChecks(ctx, comboOrg.UID, &models.ListChecksFilter{
			Query: "http",
			Limit: 2,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(4), total, "Total should count all matching checks")
		assert.Len(t, results, 3, "DB returns limit+1 when there are more")
	})

	testChecksWithOrgDelete(ctx, t, svc, org)
}

func testChecksWithOrgDelete(ctx context.Context, t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	t.Run("Delete", func(t *testing.T) {
		check := models.NewCheck(org.UID, "delete-check", "http")
		err := svc.CreateCheck(ctx, check)
		require.NoError(t, err)

		err = svc.DeleteCheck(ctx, check.UID)
		require.NoError(t, err)

		_, err = svc.GetCheck(ctx, org.UID, check.UID)
		assert.Error(t, err)
	})

	t.Run("GetCheckWrongOrg", func(t *testing.T) {
		r := require.New(t)

		// Create a check in the org
		check := models.NewCheck(org.UID, "wrong-org-check", "http")
		err := svc.CreateCheck(ctx, check)
		r.NoError(err)

		// Try to get it with wrong org UID
		_, err = svc.GetCheck(ctx, "wrong-org-uid", check.UID)
		r.Error(err, "GetCheck should fail with wrong org UID")

		// Same for GetCheckByUidOrSlug
		_, err = svc.GetCheckByUidOrSlug(ctx, "wrong-org-uid", check.UID)
		r.Error(err, "GetCheckByUidOrSlug should fail with wrong org UID")
	})

	// Reproduces issue #129: deleting a check used to leave its
	// check_dependencies edges (as either parent or child) alive forever,
	// since GetCheck's deleted_at filter made the check invisible without
	// the FK's ON DELETE CASCADE ever firing (checks are only soft-deleted).
	// DeleteCheckDependenciesForCheck is what checks.Service.DeleteCheck now
	// calls to clean those edges up.
	t.Run("DeleteCheckDependenciesForCheck", func(t *testing.T) {
		r := require.New(t)

		upstream := models.NewCheck(org.UID, "dep-upstream", "http")
		r.NoError(svc.CreateCheck(ctx, upstream))
		middle := models.NewCheck(org.UID, "dep-middle", "http")
		r.NoError(svc.CreateCheck(ctx, middle))
		downstream := models.NewCheck(org.UID, "dep-downstream", "http")
		r.NoError(svc.CreateCheck(ctx, downstream))
		sibling := models.NewCheck(org.UID, "dep-sibling", "http")
		r.NoError(svc.CreateCheck(ctx, sibling))

		// middle depends on upstream (middle is child, upstream is parent);
		// downstream depends on middle (downstream is child, middle is parent).
		// So `middle` appears as both a parent and a child. `sibling` depends
		// on `upstream` too, as a completely unrelated edge that must survive.
		edgeUp := models.NewCheckDependency(
			org.UID, upstream.UID, middle.UID, models.CheckDependencyKindHard, nil,
		)
		r.NoError(svc.CreateCheckDependency(ctx, edgeUp))
		edgeDown := models.NewCheckDependency(
			org.UID, middle.UID, downstream.UID, models.CheckDependencyKindHard, nil,
		)
		r.NoError(svc.CreateCheckDependency(ctx, edgeDown))
		edgeUnrelated := models.NewCheckDependency(
			org.UID, upstream.UID, sibling.UID, models.CheckDependencyKindHard, nil,
		)
		r.NoError(svc.CreateCheckDependency(ctx, edgeUnrelated))

		err := svc.DeleteCheckDependenciesForCheck(ctx, middle.UID)
		r.NoError(err)

		parentsOfDownstream, err := svc.ListCheckDependencyParents(ctx, downstream.UID)
		r.NoError(err)
		r.Empty(parentsOfDownstream, "edge where middle is the parent should be gone")

		childrenOfUpstream, err := svc.ListCheckDependencyChildren(ctx, upstream.UID)
		r.NoError(err)
		r.Len(childrenOfUpstream, 1, "only middle's edge should be gone, sibling's should survive")
		r.Equal(sibling.UID, childrenOfUpstream[0].ChildCheckUID)

		unrelatedEdge, err := svc.FindCheckDependencyEdge(ctx, upstream.UID, sibling.UID)
		r.NoError(err, "the unrelated upstream->sibling edge must survive")
		r.NotNil(unrelatedEdge)
	})
}

func testResultsWithCheckAndOrg(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	// Create org and check first
	org := models.NewOrganization("result-test-org", "")
	err := svc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	check := models.NewCheck(org.UID, "result-check", "http")
	err = svc.CreateCheck(ctx, check)
	require.NoError(t, err)

	t.Run("CreateAndGet", func(t *testing.T) {
		result := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.150)
		region := "us-east-1"
		result.Region = &region
		result.Output = models.JSONMap{"response_code": 200}

		err := svc.CreateResult(ctx, result)
		require.NoError(t, err)

		retrieved, err := svc.GetResult(ctx, result.UID)
		require.NoError(t, err)
		assert.Equal(t, result.UID, retrieved.UID)
		assert.Equal(t, int(models.ResultStatusUp), *retrieved.Status)
		assert.InDelta(t, 0.150, *retrieved.Duration, 0.001)
		assert.Equal(t, "us-east-1", *retrieved.Region)
	})

	t.Run("List", func(t *testing.T) {
		result1 := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.100)
		result2 := models.NewResult(org.UID, check.UID, models.ResultStatusDown, 0)
		result3 := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.200)

		err := svc.CreateResult(ctx, result1)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps

		err = svc.CreateResult(ctx, result2)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		err = svc.CreateResult(ctx, result3)
		require.NoError(t, err)

		resultsResp, err := svc.ListResults(ctx, &models.ListResultsFilter{
			OrganizationUID: org.UID,
			CheckUIDs:       []string{check.UID},
			Limit:           10,
		})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(resultsResp.Results), 3)
	})

	t.Run("ListWithLimit", func(t *testing.T) {
		resultsResp, err := svc.ListResults(ctx, &models.ListResultsFilter{
			OrganizationUID: org.UID,
			CheckUIDs:       []string{check.UID},
			Limit:           2,
		})
		require.NoError(t, err)
		assert.LessOrEqual(t, len(resultsResp.Results), 2)
	})
}

// testResultStatusConstraint is the cross-engine regression guard for the
// widened results.status CHECK constraint (status in 0..8). It pins that the
// new live Error(6) and Warning(8) raw rows and the aggregated Degraded(7) row
// all persist and round-trip on both Postgres and SQLite — the 6 case is a
// regression for the previously-too-narrow constraint (0..5).
func testResultStatusConstraint(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	org := models.NewOrganization("status-cstr-org", "")
	require.NoError(t, svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "status-cstr-check", "http")
	require.NoError(t, svc.CreateCheck(ctx, check))

	tests := []struct {
		name   string
		status models.ResultStatus
	}{
		{"Error6", models.ResultStatusError},
		{"Degraded7", models.ResultStatusDegraded},
		{"Warning8", models.ResultStatusWarning},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := models.NewResult(org.UID, check.UID, tc.status, 0.1)
			require.NoError(t, svc.CreateResult(ctx, result),
				"status %d must satisfy the widened results.status CHECK", int(tc.status))

			retrieved, err := svc.GetResult(ctx, result.UID)
			require.NoError(t, err)
			require.NotNil(t, retrieved.Status)
			require.Equal(t, int(tc.status), *retrieved.Status)
		})
	}
}

func testJSONMapHandling(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	t.Run("WorkerWithRegion", func(t *testing.T) {
		worker := models.NewWorker("json-worker", "JSON Test Worker")
		region := "eu-west-1"
		worker.Region = &region

		err := svc.CreateWorker(ctx, worker)
		require.NoError(t, err)

		retrieved, err := svc.GetWorker(ctx, worker.UID)
		require.NoError(t, err)

		assert.Equal(t, "eu-west-1", *retrieved.Region)
	})

	t.Run("WorkerWithoutRegion", func(t *testing.T) {
		worker := models.NewWorker("empty-region", "Empty Region Worker")
		// Region is nil by default

		err := svc.CreateWorker(ctx, worker)
		require.NoError(t, err)

		retrieved, err := svc.GetWorker(ctx, worker.UID)
		require.NoError(t, err)
		assert.Nil(t, retrieved.Region)
	})
}

func TestPostgresService(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL test in short mode")
	}

	svc, err := postgres.NewEmbedded(t.Context(), "db-service", 5435, false, "", false, 0)
	require.NoError(t, err, "Failed to create PostgreSQL service")

	t.Cleanup(func() {
		_ = svc.Close()
	})

	testService(t, svc)
}

func TestSQLiteService(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "sqlite-test-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	svc, err := sqlite.New(t.Context(), sqlite.Config{DataDir: tempDir})
	require.NoError(t, err, "Failed to create SQLite service")

	t.Cleanup(func() {
		_ = svc.Close()
	})

	testService(t, svc)
}

func TestSQLiteServiceInMemory(t *testing.T) {
	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err, "Failed to create in-memory SQLite service")

	t.Cleanup(func() {
		_ = svc.Close()
	})

	testService(t, svc)
}

func testJobsWithOrg(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	// Create an organization first
	org := models.NewOrganization("job-test-org", "")
	err := svc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	testJobsBasicOps(ctx, t, svc, org)
	testJobsUpdateAndDelete(ctx, t, svc, org)
	testJobsAdvanced(ctx, t, svc, org)
}

func testJobsBasicOps(ctx context.Context, t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	t.Run("CreateAndGet", func(t *testing.T) {
		job := models.NewJob(&org.UID, "test-job")
		job.Config = models.JSONMap{"key": "value"}

		err := svc.CreateJob(ctx, job)
		require.NoError(t, err)

		retrieved, err := svc.GetJob(ctx, job.UID)
		require.NoError(t, err)
		assert.Equal(t, job.UID, retrieved.UID)
		assert.Equal(t, &org.UID, retrieved.OrganizationUID)
		assert.Equal(t, "test-job", retrieved.Type)
		assert.Equal(t, models.JobStatusPending, retrieved.Status)
		assert.Equal(t, 0, retrieved.RetryCount)
		assert.Equal(t, "value", retrieved.Config["key"])
	})

	t.Run("List", func(t *testing.T) {
		job1 := models.NewJob(&org.UID, "list-job-1")
		job2 := models.NewJob(&org.UID, "list-job-2")

		err := svc.CreateJob(ctx, job1)
		require.NoError(t, err)
		err = svc.CreateJob(ctx, job2)
		require.NoError(t, err)

		jobs, err := svc.ListJobs(ctx, &org.UID, 0)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(jobs), 2)
	})

	t.Run("ListWithLimit", func(t *testing.T) {
		jobs, err := svc.ListJobs(ctx, &org.UID, 2)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(jobs), 2)
	})
}

func testJobsUpdateAndDelete(ctx context.Context, t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	t.Run("Update", func(t *testing.T) {
		job := models.NewJob(&org.UID, "update-job")
		err := svc.CreateJob(ctx, job)
		require.NoError(t, err)

		newStatus := models.JobStatusRunning
		newRetryCount := 1
		newConfig := models.JSONMap{"updated": "true"}
		newOutput := models.JSONMap{"result": "success"}

		err = svc.UpdateJob(ctx, job.UID, models.JobUpdate{
			Status:     &newStatus,
			RetryCount: &newRetryCount,
			Config:     &newConfig,
			Output:     &newOutput,
		})
		require.NoError(t, err)

		updated, err := svc.GetJob(ctx, job.UID)
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusRunning, updated.Status)
		assert.Equal(t, 1, updated.RetryCount)
		assert.Equal(t, "true", updated.Config["updated"])
		assert.Equal(t, "success", updated.Output["result"])
	})

	t.Run("Delete", func(t *testing.T) {
		job := models.NewJob(&org.UID, "delete-job")
		err := svc.CreateJob(ctx, job)
		require.NoError(t, err)

		err = svc.DeleteJob(ctx, job.UID)
		require.NoError(t, err)

		_, err = svc.GetJob(ctx, job.UID)
		assert.Error(t, err)
	})
}

func testJobsAdvanced(ctx context.Context, t *testing.T, svc db.Service, org *models.Organization) {
	t.Helper()

	t.Run("StatusTransitions", func(t *testing.T) {
		job := models.NewJob(&org.UID, "status-transition-job")
		err := svc.CreateJob(ctx, job)
		require.NoError(t, err)

		// Pending -> Running
		runningStatus := models.JobStatusRunning
		err = svc.UpdateJob(ctx, job.UID, models.JobUpdate{Status: &runningStatus})
		require.NoError(t, err)

		retrieved, err := svc.GetJob(ctx, job.UID)
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusRunning, retrieved.Status)

		// Running -> Success
		successStatus := models.JobStatusSuccess
		err = svc.UpdateJob(ctx, job.UID, models.JobUpdate{Status: &successStatus})
		require.NoError(t, err)

		retrieved, err = svc.GetJob(ctx, job.UID)
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusSuccess, retrieved.Status)
	})

	t.Run("RetryChain", func(t *testing.T) {
		job1 := models.NewJob(&org.UID, "retry-job-1")
		err := svc.CreateJob(ctx, job1)
		require.NoError(t, err)

		// Create a retry job that references the first job
		job2 := models.NewJob(&org.UID, "retry-job-2")
		job2.PreviousJobUID = &job1.UID
		job2.RetryCount = 1
		err = svc.CreateJob(ctx, job2)
		require.NoError(t, err)

		retrieved, err := svc.GetJob(ctx, job2.UID)
		require.NoError(t, err)
		assert.Equal(t, &job1.UID, retrieved.PreviousJobUID)
		assert.Equal(t, 1, retrieved.RetryCount)
	})
}

func testJobsWithoutOrg(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	t.Run("CreateJobWithoutOrg", func(t *testing.T) {
		job := models.NewJob(nil, "global-job")
		job.Config = models.JSONMap{"global": "true"}

		err := svc.CreateJob(ctx, job)
		require.NoError(t, err)

		retrieved, err := svc.GetJob(ctx, job.UID)
		require.NoError(t, err)
		assert.Equal(t, job.UID, retrieved.UID)
		assert.Nil(t, retrieved.OrganizationUID)
		assert.Equal(t, "global-job", retrieved.Type)
		assert.Equal(t, "true", retrieved.Config["global"])
	})

	t.Run("ListAllJobs", func(t *testing.T) {
		// Create a global job
		globalJob := models.NewJob(nil, "list-global-job")
		err := svc.CreateJob(ctx, globalJob)
		require.NoError(t, err)

		// List all jobs (without org filter)
		jobs, err := svc.ListJobs(ctx, nil, 0)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(jobs), 1)

		// Check that we can find jobs with and without organizations
		foundGlobal := false

		for _, job := range jobs {
			if job.UID == globalJob.UID {
				foundGlobal = true

				assert.Nil(t, job.OrganizationUID)
			}
		}

		assert.True(t, foundGlobal, "Should find the global job in the list")
	})
}

// testJobsCleanupRetention is the cross-engine parity guard for the jobs_cleanup
// two-stage retention DB methods (spec 2026-07-11-17): stage 1
// (SoftDeleteFinishedJobs) and stage 2 (DeleteSoftDeletedJobs) must behave
// identically on Postgres and SQLite. Every assertion targets a seeded UID so
// the shared harness DB cannot pollute it (the methods scan the whole table).
func testJobsCleanupRetention(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)
	now := time.Now()

	// insert writes a job with fully controlled status/timestamps/link, bypassing
	// CreateJob's defaults. deletedAgo nil means a live row; prev links a retry
	// chain via previous_job_uid.
	insert := func(
		status models.JobStatus, updatedAgo time.Duration, deletedAgo *time.Duration, prev *string,
	) *models.Job {
		job := models.NewJob(nil, "jobs-cleanup-test")
		job.Status = status
		job.UpdatedAt = now.Add(-updatedAgo)
		job.CreatedAt = job.UpdatedAt
		if deletedAgo != nil {
			d := now.Add(-*deletedAgo)
			job.DeletedAt = &d
		}
		job.PreviousJobUID = prev

		_, err := svc.DB().NewInsert().Model(job).Exec(ctx)
		r.NoError(err)

		return job
	}
	reload := func(uid string) *models.Job {
		var job models.Job
		err := svc.DB().NewSelect().Model(&job).Where("uid = ?", uid).Scan(ctx)
		r.NoError(err)

		return &job
	}
	exists := func(uid string) bool {
		n, err := svc.DB().NewSelect().Model((*models.Job)(nil)).Where("uid = ?", uid).Count(ctx)
		r.NoError(err)

		return n > 0
	}

	h24 := 24 * time.Hour
	h25 := 25 * time.Hour
	h23 := 23 * time.Hour
	softBoundary := now.Add(-48 * time.Hour)
	hardBoundary := now.Add(-h24)

	// Stage-1 candidates: three terminal statuses done > 48h ago (should be
	// soft-deleted) versus rows that must survive stage 1.
	oldSuccess := insert(models.JobStatusSuccess, 49*time.Hour, nil, nil)
	oldRetried := insert(models.JobStatusRetried, 49*time.Hour, nil, nil)
	oldFailed := insert(models.JobStatusFailed, 49*time.Hour, nil, nil)
	recentSuccess := insert(models.JobStatusSuccess, 47*time.Hour, nil, nil) // 47h < 48h → survives
	oldPending := insert(models.JobStatusPending, 49*time.Hour, nil, nil)    // never touched, any age
	oldRunning := insert(models.JobStatusRunning, 49*time.Hour, nil, nil)    // never touched, any age
	alreadySoft := insert(models.JobStatusSuccess, 49*time.Hour, &h25, nil)  // stage 1 must not re-touch
	alreadySoftAt := reload(alreadySoft.UID).DeletedAt

	// Stage-2 candidates: rows soft-deleted > 24h ago (hard-deleted) versus a
	// freshly soft-deleted one; userCancelled models a cancel that set deleted_at
	// on a still-pending row.
	hardEligible := insert(models.JobStatusSuccess, 49*time.Hour, &h25, nil)
	hardFresh := insert(models.JobStatusSuccess, 49*time.Hour, &h23, nil) // soft-deleted 23h → survives
	userCanceled := insert(models.JobStatusPending, time.Hour, &h25, nil) // canceled, deleted_at old

	// Retry chain: successor references predecessor; the FK guard defers the
	// predecessor's hard delete until the successor is gone.
	predecessor := insert(models.JobStatusRetried, 49*time.Hour, &h25, nil)
	successor := insert(models.JobStatusSuccess, 49*time.Hour, &h25, &predecessor.UID)

	// --- Stage 1: soft-delete finished jobs done > 48h ago. ---
	soft, err := svc.SoftDeleteFinishedJobs(ctx, softBoundary, 1000)
	r.NoError(err)
	r.GreaterOrEqual(soft, int64(3), "at least the three old terminal rows soft-deleted")

	r.NotNil(reload(oldSuccess.UID).DeletedAt, "old success soft-deleted")
	r.NotNil(reload(oldRetried.UID).DeletedAt, "old retried soft-deleted")
	r.NotNil(reload(oldFailed.UID).DeletedAt, "old failed soft-deleted")
	r.Nil(reload(recentSuccess.UID).DeletedAt, "success done 47h ago survives (< 48h)")
	r.Nil(reload(oldPending.UID).DeletedAt, "pending never soft-deleted regardless of age")
	r.Nil(reload(oldRunning.UID).DeletedAt, "running never soft-deleted regardless of age")
	r.WithinDuration(*alreadySoftAt, *reload(alreadySoft.UID).DeletedAt, time.Second,
		"stage 1 must not re-stamp an already soft-deleted row")

	// Idempotency: a second stage-1 pass finds nothing new among our rows (the
	// freshly soft-deleted ones now carry deleted_at, and the survivors stay
	// ineligible). It must not resurrect or re-touch anything.
	freshDeletedAt := reload(oldSuccess.UID).DeletedAt
	_, err = svc.SoftDeleteFinishedJobs(ctx, softBoundary, 1000)
	r.NoError(err)
	r.WithinDuration(*freshDeletedAt, *reload(oldSuccess.UID).DeletedAt, time.Second,
		"second stage-1 pass leaves the already soft-deleted row's timestamp intact")

	// --- Stage 2, pass 1: hard-delete rows soft-deleted > 24h ago. ---
	hard, err := svc.DeleteSoftDeletedJobs(ctx, hardBoundary, 1000)
	r.NoError(err)
	r.GreaterOrEqual(hard, int64(1))

	r.False(exists(hardEligible.UID), "row soft-deleted 25h ago is hard-deleted")
	r.False(exists(userCanceled.UID), "user-canceled row (deleted_at 25h ago) is hard-deleted")
	r.True(exists(hardFresh.UID), "row soft-deleted only 23h ago survives the grace window")
	r.True(exists(recentSuccess.UID), "still-visible finished row untouched by stage 2")
	// The rows soft-deleted by stage 1 just now (deleted_at ~= now) are NOT past
	// the 24h grace window, so they remain queryable.
	r.True(exists(oldSuccess.UID), "freshly soft-deleted row stays queryable during grace window")

	// Retry chain: successor (unreferenced) goes first; predecessor is deferred.
	r.False(exists(successor.UID), "unreferenced successor hard-deleted")
	r.True(exists(predecessor.UID), "referenced predecessor deferred by the FK guard")

	// --- Stage 2, pass 2: the now-unreferenced predecessor drains. ---
	_, err = svc.DeleteSoftDeletedJobs(ctx, hardBoundary, 1000)
	r.NoError(err)
	r.False(exists(predecessor.UID), "predecessor drains once its successor is gone (two runs)")

	// Idempotency: a third stage-2 pass deletes nothing more of ours.
	r.True(exists(hardFresh.UID))
	_, err = svc.DeleteSoftDeletedJobs(ctx, hardBoundary, 1000)
	r.NoError(err)
	r.True(exists(hardFresh.UID), "stage 2 remains a no-op for rows inside the grace window")

	// --- Batching contract: LIMIT bounds each call, a short batch signals done. ---
	for i := 0; i < 5; i++ {
		insert(models.JobStatusSuccess, 49*time.Hour, nil, nil)
	}

	b1, err := svc.SoftDeleteFinishedJobs(ctx, softBoundary, 2)
	r.NoError(err)
	r.Equal(int64(2), b1, "first batch is full (limit)")
	b2, err := svc.SoftDeleteFinishedJobs(ctx, softBoundary, 2)
	r.NoError(err)
	r.Equal(int64(2), b2, "second batch is full (limit)")
	b3, err := svc.SoftDeleteFinishedJobs(ctx, softBoundary, 2)
	r.NoError(err)
	r.Equal(int64(1), b3, "third batch is short → backlog drained")
	b4, err := svc.SoftDeleteFinishedJobs(ctx, softBoundary, 2)
	r.NoError(err)
	r.Equal(int64(0), b4, "nothing left to soft-delete")
}

func testStateEntries(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	// Create an organization first
	org := models.NewOrganization("state-test-org", "")
	err := svc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	orgUID := &org.UID // Use pointer for state entry calls

	t.Run("SetAndGet", func(t *testing.T) {
		r := require.New(t)

		key := models.StateKey("test", "entry", "1")
		value := &models.JSONMap{"channel_id": "C123", "thread_ts": "1234.5678"}

		err := svc.SetStateEntry(ctx, orgUID, key, value, nil)
		r.NoError(err)

		retrieved, err := svc.GetStateEntry(ctx, orgUID, key)
		r.NoError(err)
		r.NotNil(retrieved)
		r.Equal(key, retrieved.Key)
		r.NotNil(retrieved.Value)
		r.Equal("C123", (*retrieved.Value)["channel_id"])
		r.Equal("1234.5678", (*retrieved.Value)["thread_ts"])
	})

	t.Run("GetNonExistent", func(t *testing.T) {
		r := require.New(t)

		retrieved, err := svc.GetStateEntry(ctx, orgUID, "non:existent:key")
		r.NoError(err, "GetStateEntry should not error for non-existent key")
		r.Nil(retrieved, "Should return nil for non-existent key")
	})

	t.Run("SetWithTTL", func(t *testing.T) {
		r := require.New(t)

		key := models.StateKey("test", "ttl", "entry")
		value := &models.JSONMap{"temp": "data"}
		ttl := 1 * time.Hour

		err := svc.SetStateEntry(ctx, orgUID, key, value, &ttl)
		r.NoError(err)

		retrieved, err := svc.GetStateEntry(ctx, orgUID, key)
		r.NoError(err)
		r.NotNil(retrieved)
		r.NotNil(retrieved.ExpiresAt, "ExpiresAt should be set")
		r.True(retrieved.ExpiresAt.After(time.Now()), "ExpiresAt should be in the future")
	})

	t.Run("SetOverwrite", func(t *testing.T) {
		r := require.New(t)

		key := models.StateKey("test", "overwrite")
		value1 := &models.JSONMap{"version": "1"}
		value2 := &models.JSONMap{"version": "2"}

		err := svc.SetStateEntry(ctx, orgUID, key, value1, nil)
		r.NoError(err)

		err = svc.SetStateEntry(ctx, orgUID, key, value2, nil)
		r.NoError(err)

		retrieved, err := svc.GetStateEntry(ctx, orgUID, key)
		r.NoError(err)
		r.NotNil(retrieved)
		r.Equal("2", (*retrieved.Value)["version"], "Value should be overwritten")
	})

	t.Run("Delete", func(t *testing.T) {
		r := require.New(t)

		key := models.StateKey("test", "delete")
		value := &models.JSONMap{"to_delete": true}

		err := svc.SetStateEntry(ctx, orgUID, key, value, nil)
		r.NoError(err)

		consumed, err := svc.DeleteStateEntry(ctx, orgUID, key)
		r.NoError(err)
		r.True(consumed, "a live entry was deleted")

		retrieved, err := svc.GetStateEntry(ctx, orgUID, key)
		r.NoError(err)
		r.Nil(retrieved, "Entry should be nil after deletion")

		// The atomic compare-and-set guard callers like OAuth auth-code
		// consumption rely on: a repeat delete of an already-gone entry
		// reports it didn't win, rather than silently no-op succeeding.
		consumedAgain, err := svc.DeleteStateEntry(ctx, orgUID, key)
		r.NoError(err)
		r.False(consumedAgain, "deleting an already-deleted entry reports no live row")
	})

	t.Run("List", func(t *testing.T) {
		r := require.New(t)

		// Create entries with a common prefix
		prefix := "list:test"
		key1 := models.StateKey(prefix, "entry1")
		key2 := models.StateKey(prefix, "entry2")
		key3 := models.StateKey("other", "entry")

		err := svc.SetStateEntry(ctx, orgUID, key1, &models.JSONMap{"idx": "1"}, nil)
		r.NoError(err)
		err = svc.SetStateEntry(ctx, orgUID, key2, &models.JSONMap{"idx": "2"}, nil)
		r.NoError(err)
		err = svc.SetStateEntry(ctx, orgUID, key3, &models.JSONMap{"idx": "3"}, nil)
		r.NoError(err)

		// List with prefix
		entries, err := svc.ListStateEntries(ctx, orgUID, prefix)
		r.NoError(err)
		r.GreaterOrEqual(len(entries), 2, "Should find at least 2 entries with prefix")

		// Verify the entries have the correct prefix
		for _, entry := range entries {
			if entry.Key == key3 {
				r.Fail("Should not find entry with different prefix")
			}
		}
	})

	t.Run("SetIfNotExists", func(t *testing.T) {
		r := require.New(t)

		key := models.StateKey("test", "setifnotexists")
		value1 := &models.JSONMap{"first": true}
		value2 := &models.JSONMap{"second": true}

		// First set should succeed
		created, err := svc.SetStateEntryIfNotExists(ctx, orgUID, key, value1, nil)
		r.NoError(err)
		r.True(created, "First set should create entry")

		// Second set should not create
		created, err = svc.SetStateEntryIfNotExists(ctx, orgUID, key, value2, nil)
		r.NoError(err)
		r.False(created, "Second set should not create entry")

		// Value should still be the first one
		retrieved, err := svc.GetStateEntry(ctx, orgUID, key)
		r.NoError(err)
		r.NotNil(retrieved)
		r.Equal(true, (*retrieved.Value)["first"], "Value should still be the first one")
	})

	t.Run("GetOrCreate", func(t *testing.T) {
		r := require.New(t)

		key := models.StateKey("test", "getorcreate")
		defaultValue := &models.JSONMap{"count": float64(0)}

		// First call should create
		entry1, created1, err := svc.GetOrCreateStateEntry(ctx, orgUID, key, defaultValue, nil)
		r.NoError(err)
		r.True(created1, "First call should create entry")
		r.NotNil(entry1)
		r.InDelta(float64(0), (*entry1.Value)["count"], 0.001)

		// Second call should return existing
		entry2, created2, err := svc.GetOrCreateStateEntry(ctx, orgUID, key, &models.JSONMap{"count": float64(999)}, nil)
		r.NoError(err)
		r.False(created2, "Second call should not create entry")
		r.NotNil(entry2)
		r.Equal(entry1.UID, entry2.UID, "Should return the same entry")
	})

	t.Run("DeleteExpired", func(t *testing.T) {
		r := require.New(t)

		// Create a new org to isolate this test
		expiredOrg := models.NewOrganization("expired-test-org", "")
		err := svc.CreateOrganization(ctx, expiredOrg)
		r.NoError(err)

		expiredOrgUID := &expiredOrg.UID

		// Create an entry with very short TTL (already expired)
		key := models.StateKey("test", "expired")
		value := &models.JSONMap{"expired": true}

		// We can't easily create an already-expired entry through the API,
		// so we test that DeleteExpiredStateEntries at least runs without error
		count, err := svc.DeleteExpiredStateEntries(ctx)
		r.NoError(err)
		// count can be 0 or more depending on state from other tests
		r.GreaterOrEqual(count, int64(0))

		// Set an entry that is NOT expired
		err = svc.SetStateEntry(ctx, expiredOrgUID, key, value, nil)
		r.NoError(err)

		// Run cleanup again - should not delete the non-expired entry
		_, err = svc.DeleteExpiredStateEntries(ctx)
		r.NoError(err)

		// Entry should still exist
		retrieved, err := svc.GetStateEntry(ctx, expiredOrgUID, key)
		r.NoError(err)
		r.NotNil(retrieved, "Non-expired entry should still exist")
	})

	t.Run("StateKey", func(t *testing.T) {
		r := require.New(t)

		// Test the StateKey helper function
		key := models.StateKey("incident", "abc123", "slack_notification")
		r.Equal("incident:abc123:slack_notification", key)

		key = models.StateKey("single")
		r.Equal("single", key)

		key = models.StateKey()
		r.Empty(key)
	})
}

// strPtr is a tiny helper to take the address of a string literal.
func strPtr(s string) *string { return &s }

//nolint:maintidx // exercises the full subscriber lifecycle against both backends
func testStatusPageSubscribers(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("sub-test-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	page := models.NewStatusPage(org.UID, "Sub Page", "sub-page")
	r.NoError(svc.CreateStatusPage(ctx, page))

	t.Run("CreateAndFetchByToken", func(_ *testing.T) {
		sub := models.NewStatusPageSubscriber(org.UID, page.UID, "a@example.com", models.SubscriberScopePage)
		sub.ConfirmToken = "confirm-tok-1"
		sub.UnsubscribeToken = "unsub-tok-1"
		r.NoError(svc.CreateSubscriber(ctx, sub))

		byConfirm, err := svc.GetSubscriberByConfirmToken(ctx, "confirm-tok-1")
		r.NoError(err)
		r.Equal(sub.UID, byConfirm.UID)
		r.Nil(byConfirm.ConfirmedAt)

		byUnsub, err := svc.GetSubscriberByUnsubToken(ctx, "unsub-tok-1")
		r.NoError(err)
		r.Equal(sub.UID, byUnsub.UID)
	})

	t.Run("FindLiveSubscriber", func(_ *testing.T) {
		found, err := svc.FindLiveSubscriber(
			ctx, page.UID, "a@example.com", models.SubscriberScopePage, nil)
		r.NoError(err)
		r.Equal("a@example.com", found.EmailAddress())
	})

	t.Run("ConfirmConsumesToken", func(_ *testing.T) {
		sub := models.NewStatusPageSubscriber(org.UID, page.UID, "b@example.com", models.SubscriberScopePage)
		sub.ConfirmToken = "confirm-tok-2"
		sub.UnsubscribeToken = "unsub-tok-2"
		r.NoError(svc.CreateSubscriber(ctx, sub))

		now := time.Now()
		r.NoError(svc.ConfirmSubscriber(ctx, sub.UID, now))

		// Confirm token is consumed (cleared) so a second lookup fails.
		_, err := svc.GetSubscriberByConfirmToken(ctx, "confirm-tok-2")
		r.Error(err)

		got, err := svc.GetSubscriber(ctx, page.UID, sub.UID)
		r.NoError(err)
		r.NotNil(got.ConfirmedAt)
	})

	t.Run("ListConfirmedScoping", func(_ *testing.T) {
		check := models.NewCheck(org.UID, "sub-incident-check", "http")
		r.NoError(svc.CreateCheck(ctx, check))

		incident := models.NewIncident(org.UID, check.UID, time.Now(), "outage")
		r.NoError(svc.CreateIncident(ctx, incident))
		incidentA := incident.UID

		// Page-scoped confirmed.
		pageSub := models.NewStatusPageSubscriber(org.UID, page.UID, "page@example.com", models.SubscriberScopePage)
		pageSub.ConfirmToken = "ct-page"
		pageSub.UnsubscribeToken = "ut-page"
		r.NoError(svc.CreateSubscriber(ctx, pageSub))
		r.NoError(svc.ConfirmSubscriber(ctx, pageSub.UID, time.Now()))

		// Incident-scoped confirmed matching incidentA.
		incSub := models.NewStatusPageSubscriber(
			org.UID, page.UID, "inc@example.com", models.SubscriberScopeIncident)
		incSub.IncidentUID = strPtr(incidentA)
		incSub.ConfirmToken = "ct-inc"
		incSub.UnsubscribeToken = "ut-inc"
		r.NoError(svc.CreateSubscriber(ctx, incSub))
		r.NoError(svc.ConfirmSubscriber(ctx, incSub.UID, time.Now()))

		// Unconfirmed page-scoped (must be excluded).
		unconf := models.NewStatusPageSubscriber(
			org.UID, page.UID, "unconf@example.com", models.SubscriberScopePage)
		unconf.ConfirmToken = "ct-unconf"
		unconf.UnsubscribeToken = "ut-unconf"
		r.NoError(svc.CreateSubscriber(ctx, unconf))

		// With incident: page-scoped + matching incident-scoped.
		withIncident, err := svc.ListConfirmedSubscribers(ctx, page.UID, &incidentA)
		r.NoError(err)
		emails := subscriberEmails(withIncident)
		r.Contains(emails, "page@example.com")
		r.Contains(emails, "inc@example.com")
		r.NotContains(emails, "unconf@example.com")

		// Without incident: only page-scoped.
		pageOnly, err := svc.ListConfirmedSubscribers(ctx, page.UID, nil)
		r.NoError(err)
		pageEmails := subscriberEmails(pageOnly)
		r.Contains(pageEmails, "page@example.com")
		r.NotContains(pageEmails, "inc@example.com")
	})

	t.Run("SoftDeleteAndResubscribe", func(_ *testing.T) {
		sub := models.NewStatusPageSubscriber(org.UID, page.UID, "c@example.com", models.SubscriberScopePage)
		sub.ConfirmToken = "ct-c"
		sub.UnsubscribeToken = "ut-c"
		r.NoError(svc.CreateSubscriber(ctx, sub))
		r.NoError(svc.ConfirmSubscriber(ctx, sub.UID, time.Now()))

		r.NoError(svc.SoftDeleteSubscriber(ctx, sub.UID))

		// No longer a live subscriber.
		_, err := svc.FindLiveSubscriber(ctx, page.UID, "c@example.com", models.SubscriberScopePage, nil)
		r.Error(err)

		// Resubscribe soft-undeletes and resets confirmation.
		r.NoError(svc.ResubscribeSubscriber(ctx, sub.UID, "ct-c2", "ut-c2"))
		revived, err := svc.FindLiveSubscriber(ctx, page.UID, "c@example.com", models.SubscriberScopePage, nil)
		r.NoError(err)
		r.Equal(sub.UID, revived.UID)
		r.Nil(revived.ConfirmedAt)
	})

	t.Run("ListSubscribersAdmin", func(_ *testing.T) {
		subs, err := svc.ListSubscribers(ctx, page.UID)
		r.NoError(err)
		r.NotEmpty(subs)
	})
}

func subscriberEmails(subs []*models.StatusPageSubscriber) []string {
	emails := make([]string, 0, len(subs))
	for _, s := range subs {
		emails = append(emails, s.EmailAddress())
	}

	return emails
}

func testAppSettings(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	t.Run("GetMissingKey", func(t *testing.T) {
		t.Parallel()

		_, err := svc.GetAppSetting(ctx, "nonexistent.key."+strconv.FormatInt(time.Now().UnixNano(), 10))
		r.Error(err, "GetAppSetting should return an error for a missing key")
	})

	t.Run("SetAndGet", func(t *testing.T) {
		t.Parallel()

		key := "test.key." + strconv.FormatInt(time.Now().UnixNano(), 10)

		err := svc.SetAppSetting(ctx, key, "value1")
		r.NoError(err, "SetAppSetting should not fail")

		val, err := svc.GetAppSetting(ctx, key)
		r.NoError(err, "GetAppSetting should not fail after set")
		r.Equal("value1", val)
	})

	t.Run("Upsert", func(t *testing.T) {
		t.Parallel()

		key := "test.upsert." + strconv.FormatInt(time.Now().UnixNano(), 10)

		err := svc.SetAppSetting(ctx, key, "first")
		r.NoError(err)

		err = svc.SetAppSetting(ctx, key, "second")
		r.NoError(err)

		val, err := svc.GetAppSetting(ctx, key)
		r.NoError(err)
		r.Equal("second", val, "upsert should overwrite with new value")
	})
}

// testChannelByPropertyForOrg covers the org-scoped connection lookup added
// for spec 2026-07-05-01: a Slack workspace (team_id) connected to two
// different orgs must resolve to each org's own row, never the other org's,
// and a soft-deleted row must not be returned.
func testChannelByPropertyForOrg(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	orgA := models.NewOrganization("cbpfo-org-a", "")
	r.NoError(svc.CreateOrganization(ctx, orgA))

	orgB := models.NewOrganization("cbpfo-org-b", "")
	r.NoError(svc.CreateOrganization(ctx, orgB))

	const teamID = "T-SHARED-WORKSPACE"

	connA := models.NewIntegration(orgA.UID, models.ConnectionTypeSlack, "Workspace (org A)")
	connA.Settings["team_id"] = teamID
	r.NoError(svc.CreateChannel(ctx, connA))

	connB := models.NewIntegration(orgB.UID, models.ConnectionTypeSlack, "Workspace (org B)")
	connB.Settings["team_id"] = teamID
	r.NoError(svc.CreateChannel(ctx, connB))

	t.Run("ResolvesEachOrgsOwnRow", func(_ *testing.T) {
		gotA, err := svc.GetChannelByPropertyForOrg(
			ctx, orgA.UID, string(models.ConnectionTypeSlack), "team_id", teamID)
		r.NoError(err)
		r.Equal(connA.UID, gotA.UID)

		gotB, err := svc.GetChannelByPropertyForOrg(
			ctx, orgB.UID, string(models.ConnectionTypeSlack), "team_id", teamID)
		r.NoError(err)
		r.Equal(connB.UID, gotB.UID)

		r.NotEqual(gotA.UID, gotB.UID, "each org must have its own connection row")
	})

	t.Run("NoRowInQueriedOrg", func(_ *testing.T) {
		orgC := models.NewOrganization("cbpfo-org-c", "")
		r.NoError(svc.CreateOrganization(ctx, orgC))

		_, err := svc.GetChannelByPropertyForOrg(
			ctx, orgC.UID, string(models.ConnectionTypeSlack), "team_id", teamID)
		r.ErrorIs(err, sql.ErrNoRows)
	})

	t.Run("ExcludesSoftDeletedRow", func(_ *testing.T) {
		orgD := models.NewOrganization("cbpfo-org-d", "")
		r.NoError(svc.CreateOrganization(ctx, orgD))

		connD := models.NewIntegration(orgD.UID, models.ConnectionTypeSlack, "Workspace (org D)")
		connD.Settings["team_id"] = "T-DELETED-WORKSPACE"
		r.NoError(svc.CreateChannel(ctx, connD))
		r.NoError(svc.DeleteChannel(ctx, connD.UID))

		_, err := svc.GetChannelByPropertyForOrg(
			ctx, orgD.UID, string(models.ConnectionTypeSlack), "team_id", "T-DELETED-WORKSPACE")
		r.ErrorIs(err, sql.ErrNoRows)
	})
}

// testListChannelsByProperty covers the cross-org listing added for spec
// 2026-07-05-02: uninstall fan-out and the inbound-routing deterministic
// fallback both need every connection for a team_id, oldest first, across
// every org — not scoped to one org like GetChannelByPropertyForOrg.
func testListChannelsByProperty(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	orgA := models.NewOrganization("lcbp-org-a", "")
	r.NoError(svc.CreateOrganization(ctx, orgA))

	orgB := models.NewOrganization("lcbp-org-b", "")
	r.NoError(svc.CreateOrganization(ctx, orgB))

	const teamID = "T-LCBP-SHARED"

	connA := models.NewIntegration(orgA.UID, models.ConnectionTypeSlack, "Workspace (org A, first)")
	connA.Settings["team_id"] = teamID
	r.NoError(svc.CreateChannel(ctx, connA))

	// Ensure a distinguishable created_at ordering across engines: some
	// backends/columns have second-level timestamp resolution, so a same-
	// instant insert could tie. A short sleep keeps the ASC-order assertion
	// meaningful without relying on sub-millisecond precision.
	time.Sleep(10 * time.Millisecond)

	connB := models.NewIntegration(orgB.UID, models.ConnectionTypeSlack, "Workspace (org B, second)")
	connB.Settings["team_id"] = teamID
	r.NoError(svc.CreateChannel(ctx, connB))

	t.Run("ReturnsAllOrgsOldestFirst", func(_ *testing.T) {
		conns, err := svc.ListChannelsByProperty(
			ctx, string(models.ConnectionTypeSlack), "team_id", teamID)
		r.NoError(err)
		r.Len(conns, 2)
		r.Equal(connA.UID, conns[0].UID, "oldest connection (org A) must be first")
		r.Equal(connB.UID, conns[1].UID, "newer connection (org B) must be second")
	})

	t.Run("EmptySliceWhenNoMatch", func(_ *testing.T) {
		conns, err := svc.ListChannelsByProperty(
			ctx, string(models.ConnectionTypeSlack), "team_id", "T-LCBP-NO-SUCH-TEAM")
		r.NoError(err, "no matches should not be an error")
		r.Empty(conns)
	})

	t.Run("ExcludesSoftDeletedRows", func(_ *testing.T) {
		orgC := models.NewOrganization("lcbp-org-c", "")
		r.NoError(svc.CreateOrganization(ctx, orgC))

		const deletedTeamID = "T-LCBP-DELETED"

		connC := models.NewIntegration(orgC.UID, models.ConnectionTypeSlack, "Workspace (org C, deleted)")
		connC.Settings["team_id"] = deletedTeamID
		r.NoError(svc.CreateChannel(ctx, connC))
		r.NoError(svc.DeleteChannel(ctx, connC.UID))

		conns, err := svc.ListChannelsByProperty(
			ctx, string(models.ConnectionTypeSlack), "team_id", deletedTeamID)
		r.NoError(err)
		r.Empty(conns)
	})

	t.Run("DoesNotLeakOtherConnTypesOrProperties", func(_ *testing.T) {
		orgD := models.NewOrganization("lcbp-org-d", "")
		r.NoError(svc.CreateOrganization(ctx, orgD))

		connD := models.NewIntegration(orgD.UID, models.ConnectionTypeDiscord, "Discord (org D)")
		connD.Settings["team_id"] = teamID
		r.NoError(svc.CreateChannel(ctx, connD))

		conns, err := svc.ListChannelsByProperty(
			ctx, string(models.ConnectionTypeSlack), "team_id", teamID)
		r.NoError(err)

		for _, c := range conns {
			r.NotEqual(connD.UID, c.UID, "a discord connection must not be returned for a slack-typed lookup")
		}
	})
}

// testEmailSuppressions is the cross-engine parity guard for the D4
// suppression subsystem (spec 2026-07-05-10): create/list/get/delete and the
// two-scope IsEmailSuppressed lookup (check-specific row OR org-wide
// check_uid-IS-NULL row) must behave identically on Postgres and SQLite,
// including the partial-unique-index enforcement of "one row per (org,
// email, check_uid)" for each scope independently.
func testEmailSuppressions(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("email-supp-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "email-supp-check", "http")
	r.NoError(svc.CreateCheck(ctx, check))

	otherCheck := models.NewCheck(org.UID, "email-supp-other-check", "http")
	r.NoError(svc.CreateCheck(ctx, otherCheck))

	t.Run("CreateListGetDelete", func(t *testing.T) {
		r := require.New(t)

		sup := models.NewEmailSuppression(org.UID, "create-list@x.test", &check.UID, models.EmailSuppressionSourceLink)
		r.NoError(svc.CreateEmailSuppression(ctx, sup))

		got, err := svc.GetEmailSuppression(ctx, org.UID, sup.UID)
		r.NoError(err)
		r.Equal(sup.Email, got.Email)
		r.Equal(check.UID, *got.CheckUID)
		r.Equal(models.EmailSuppressionSourceLink, got.Source)

		list, err := svc.ListEmailSuppressions(ctx, org.UID)
		r.NoError(err)

		found := false

		for _, row := range list {
			if row.UID == sup.UID {
				found = true
			}
		}

		r.True(found, "created suppression must appear in the org's list")

		r.NoError(svc.DeleteEmailSuppression(ctx, sup.UID))

		_, err = svc.GetEmailSuppression(ctx, org.UID, sup.UID)
		r.ErrorIs(err, sql.ErrNoRows, "deleted suppression must no longer be gettable")
	})

	t.Run("GetScopedToOrg", func(t *testing.T) {
		r := require.New(t)

		otherOrg := models.NewOrganization("email-supp-other-org", "")
		r.NoError(svc.CreateOrganization(ctx, otherOrg))

		sup := models.NewEmailSuppression(org.UID, "scoped@x.test", nil, models.EmailSuppressionSourceLink)
		r.NoError(svc.CreateEmailSuppression(ctx, sup))

		_, err := svc.GetEmailSuppression(ctx, otherOrg.UID, sup.UID)
		r.Error(err, "a suppression must not be gettable from a different org")

		got, err := svc.GetEmailSuppression(ctx, org.UID, sup.UID)
		r.NoError(err)
		r.Equal(sup.UID, got.UID)
	})

	t.Run("UniquePerCheckScope", func(t *testing.T) {
		r := require.New(t)

		email := "unique-check-scope@x.test"
		first := models.NewEmailSuppression(org.UID, email, &check.UID, models.EmailSuppressionSourceLink)
		r.NoError(svc.CreateEmailSuppression(ctx, first))

		// Same (org, email, check) again must violate the partial unique index.
		dup := models.NewEmailSuppression(org.UID, email, &check.UID, models.EmailSuppressionSourceLink)
		r.Error(svc.CreateEmailSuppression(ctx, dup),
			"a second suppression for the same (org, email, check_uid) must be rejected")

		// The SAME email suppressed for a DIFFERENT check must be allowed —
		// check-scoped suppressions are independent per check.
		otherCheckSup := models.NewEmailSuppression(org.UID, email, &otherCheck.UID, models.EmailSuppressionSourceLink)
		r.NoError(svc.CreateEmailSuppression(ctx, otherCheckSup),
			"the same email suppressed for a different check must be a distinct row")
	})

	t.Run("UniquePerOrgScope", func(t *testing.T) {
		r := require.New(t)

		email := "unique-org-scope@x.test"
		first := models.NewEmailSuppression(org.UID, email, nil, models.EmailSuppressionSourceLink)
		r.NoError(svc.CreateEmailSuppression(ctx, first))

		// A second org-wide (check_uid NULL) suppression for the same email
		// must violate the partial unique index — this is the property that
		// motivated splitting into two partial indexes rather than one plain
		// unique index (a plain index treats every NULL as distinct).
		dup := models.NewEmailSuppression(org.UID, email, nil, models.EmailSuppressionSourceLink)
		r.Error(svc.CreateEmailSuppression(ctx, dup),
			"a second org-wide suppression for the same (org, email) must be rejected")
	})

	t.Run("IsEmailSuppressed", func(t *testing.T) {
		r := require.New(t)

		checkScoped := models.NewCheck(org.UID, "email-supp-ies-check", "http")
		r.NoError(svc.CreateCheck(ctx, checkScoped))

		unrelatedCheck := models.NewCheck(org.UID, "email-supp-ies-unrelated-check", "http")
		r.NoError(svc.CreateCheck(ctx, unrelatedCheck))

		t.Run("NotSuppressedByDefault", func(_ *testing.T) {
			suppressed, err := svc.IsEmailSuppressed(ctx, org.UID, "never-suppressed@x.test", checkScoped.UID)
			r.NoError(err)
			r.False(suppressed)
		})

		t.Run("CheckSpecificSuppressionOnlyAppliesToThatCheck", func(_ *testing.T) {
			email := "check-specific@x.test"
			sup := models.NewEmailSuppression(org.UID, email, &checkScoped.UID, models.EmailSuppressionSourceLink)
			r.NoError(svc.CreateEmailSuppression(ctx, sup))

			suppressed, err := svc.IsEmailSuppressed(ctx, org.UID, email, checkScoped.UID)
			r.NoError(err)
			r.True(suppressed, "must be suppressed for the check it was scoped to")

			suppressed, err = svc.IsEmailSuppressed(ctx, org.UID, email, unrelatedCheck.UID)
			r.NoError(err)
			r.False(suppressed, "must NOT be suppressed for an unrelated check")
		})

		t.Run("OrgWideSuppressionAppliesToEveryCheck", func(_ *testing.T) {
			email := "org-wide@x.test"
			sup := models.NewEmailSuppression(org.UID, email, nil, models.EmailSuppressionSourceLink)
			r.NoError(svc.CreateEmailSuppression(ctx, sup))

			suppressed, err := svc.IsEmailSuppressed(ctx, org.UID, email, checkScoped.UID)
			r.NoError(err)
			r.True(suppressed, "org-wide suppression must apply to any check")

			suppressed, err = svc.IsEmailSuppressed(ctx, org.UID, email, unrelatedCheck.UID)
			r.NoError(err)
			r.True(suppressed, "org-wide suppression must apply to every check, including ones added later")
		})

		t.Run("DoesNotLeakAcrossOrgs", func(_ *testing.T) {
			otherOrg := models.NewOrganization("email-supp-ies-other", "")
			r.NoError(svc.CreateOrganization(ctx, otherOrg))

			email := "cross-org@x.test"
			sup := models.NewEmailSuppression(org.UID, email, nil, models.EmailSuppressionSourceLink)
			r.NoError(svc.CreateEmailSuppression(ctx, sup))

			suppressed, err := svc.IsEmailSuppressed(ctx, otherOrg.UID, email, "")
			r.NoError(err)
			r.False(suppressed, "a suppression in one org must not suppress the same email in another org")
		})
	})
}

// testEventsKeysetPaginationTieBreak runs the audit trail's cursor pagination
// against BOTH engines with every row sharing one created_at.
//
// This lives in the cross-engine harness rather than beside the events service
// because it is a question about the STORE: the keyset predicate is
// `created_at < ? OR (created_at = ? AND uid < ?)`, which is only correct when
// rows sharing a created_at have a defined order. SQLite happens to return
// insertion order for a small table even without the tie-break, so a
// SQLite-only test passes either way and proves nothing — Postgres is where an
// unordered tie actually skips rows, and it is what production runs.
//
// Audit rows tie constantly: a config apply, an SSO backfill, or a burst of
// failed logins all insert within the same microsecond.
func testEventsKeysetPaginationTieBreak(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("evtie-"+uuid.New().String()[:8], "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	// One instant, twenty events. Not "close together" — identical.
	at := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)

	seeded := map[string]bool{}

	for i := 0; i < 20; i++ {
		event := models.NewEvent(org.UID, models.EventTypeCheckCreated, models.ActorTypeSystem)
		event.CreatedAt = at
		r.NoError(svc.CreateEvent(ctx, event))
		seeded[event.UID] = true
	}

	r.Len(seeded, 20, "positive control: twenty distinct events were seeded")

	const pageSize = 6

	seen := map[string]int{}

	var (
		cursorTS  *time.Time
		cursorUID *string
	)

	for page := 0; page < 10; page++ {
		rows, err := svc.ListEvents(ctx, &models.ListEventsFilter{
			OrganizationUID: org.UID,
			Limit:           pageSize,
			CursorTimestamp: cursorTS,
			CursorUID:       cursorUID,
		})
		r.NoError(err)

		if len(rows) == 0 {
			break
		}

		for _, row := range rows {
			seen[row.UID]++
		}

		last := rows[len(rows)-1]
		ts := last.CreatedAt
		uid := last.UID
		cursorTS = &ts
		cursorUID = &uid

		if len(rows) < pageSize {
			break
		}
	}

	// The load-bearing assertion: nothing skipped, nothing duplicated.
	r.Lenf(seen, 20, "expected all 20 tied events across the pages, saw %d", len(seen))

	for uid := range seeded {
		r.Equalf(1, seen[uid],
			"event %s sharing a created_at with 19 others must appear exactly once", uid)
	}
}

// testEventsTargetPayloadFilters runs the target filters against BOTH engines.
//
// They are the only filters in this table that reach INTO the payload rather
// than into a column — Postgres spells it `payload->>'target_uid'`, SQLite
// spells it `json_extract(payload, '$.target_uid')`. Two different expressions
// for one contract is exactly the shape of change that works on the engine you
// developed against and silently matches nothing on the other, and "the audit
// page's target filter returns nothing" is a bug nobody would suspect the
// dialect for.
func testEventsTargetPayloadFilters(ctx context.Context, t *testing.T, svc db.Service) {
	t.Helper()

	r := require.New(t)

	org := models.NewOrganization("evtgt-"+uuid.New().String()[:8], "Acme")
	r.NoError(svc.CreateOrganization(ctx, org))

	seed := func(eventType models.EventType, targetType, targetUID string) {
		event := models.NewEvent(org.UID, eventType, models.ActorTypeSystem)
		event.Payload = models.JSONMap{"target_type": targetType, "target_uid": targetUID}
		r.NoError(svc.CreateEvent(ctx, event))
	}

	seed(models.EventTypeIntegrationUpdated, "integration", "int-1")
	seed(models.EventTypeIntegrationDeleted, "integration", "int-2")
	seed(models.EventTypeMemberRemoved, "member", "user-9")

	targetUID := "int-1"
	byUID, err := svc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		TargetUID:       &targetUID,
		Limit:           50,
	})
	r.NoError(err)
	r.Len(byUID, 1)
	r.Equal(models.EventTypeIntegrationUpdated, byUID[0].EventType)

	targetType := "integration"
	byType, err := svc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		TargetType:      &targetType,
		Limit:           50,
	})
	r.NoError(err)
	r.Len(byType, 2)

	// Positive control: unfiltered, all three come back — so the two
	// assertions above are testing the predicates, not an empty table or a
	// json function that silently errors into "no rows" on this engine.
	all, err := svc.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: org.UID,
		Limit:           50,
	})
	r.NoError(err)
	r.Len(all, 3)
}
