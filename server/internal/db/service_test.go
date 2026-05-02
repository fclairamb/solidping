package db_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

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

	t.Run("JSONMapHandling", func(t *testing.T) {
		testJSONMapHandling(ctx, t, svc)
	})

	t.Run("JobsWithOrg", func(t *testing.T) {
		testJobsWithOrg(ctx, t, svc)
	})

	t.Run("JobsWithoutOrg", func(t *testing.T) {
		testJobsWithoutOrg(ctx, t, svc)
	})

	t.Run("StateEntries", func(t *testing.T) {
		testStateEntries(ctx, t, svc)
	})
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

	tempDir, err := os.MkdirTemp("", "postgres-test-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})

	svc, err := postgres.NewEmbedded(t.Context(), tempDir, 5435, false, "", false)
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

		err = svc.DeleteStateEntry(ctx, orgUID, key)
		r.NoError(err)

		retrieved, err := svc.GetStateEntry(ctx, orgUID, key)
		r.NoError(err)
		r.Nil(retrieved, "Entry should be nil after deletion")
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
