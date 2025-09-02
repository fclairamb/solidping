package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/members"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// memberResponse represents the API response for a member.
type memberResponse struct {
	UID       string     `json:"uid"`
	UserUID   string     `json:"userUid"`
	Email     string     `json:"email"`
	Name      string     `json:"name,omitempty"`
	AvatarURL string     `json:"avatarUrl,omitempty"`
	Role      string     `json:"role"`
	JoinedAt  *time.Time `json:"joinedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// listMembersResponse represents the API response for listing members.
type listMembersResponse struct {
	Data []*memberResponse `json:"data"`
}

// memberTestHelper holds the auth token for making member API requests.
type memberTestHelper struct {
	testServer *TestServer
	token      string
}

func newMemberTestHelper(t *testing.T, testServer *TestServer) *memberTestHelper {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	// Login and get token
	apiClient := testServer.NewClient()
	loginResp, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	r.NoError(err)
	r.NotNil(loginResp.AccessToken)

	return &memberTestHelper{
		testServer: testServer,
		token:      *loginResp.AccessToken,
	}
}

func (h *memberTestHelper) doRequest(
	t *testing.T, method, path string, body []byte,
) (*http.Response, error) {
	t.Helper()

	ctx := t.Context()

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, h.testServer.HTTPServer.URL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}

	client := &http.Client{}

	return client.Do(req)
}

func TestListMembers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// List members
	resp, err := helper.doRequest(t, "GET", "/api/v1/orgs/"+TestOrgSlug+"/members", nil)
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)

	var listResp listMembersResponse
	err = json.NewDecoder(resp.Body).Decode(&listResp)
	r.NoError(err)
	r.NoError(resp.Body.Close())

	// Should have at least the test user as a member
	r.NotEmpty(listResp.Data)

	// Find the test user in the members
	var foundTestUser bool
	for _, member := range listResp.Data {
		if member.Email == TestUserEmail {
			foundTestUser = true
			r.Equal("admin", member.Role)
			break
		}
	}
	r.True(foundTestUser, "test user should be in the members list")
}

func TestAddMember(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Create a second user to add
	secondUser := createSecondTestUser(t, testServer)

	// Add member
	addReq := members.AddMemberRequest{
		Email: secondUser.Email,
		Role:  "user",
	}
	reqBody, err := json.Marshal(addReq)
	r.NoError(err)

	resp, err := helper.doRequest(t, "POST", "/api/v1/orgs/"+TestOrgSlug+"/members", reqBody)
	r.NoError(err)
	r.Equal(http.StatusCreated, resp.StatusCode)

	var memberResp memberResponse
	err = json.NewDecoder(resp.Body).Decode(&memberResp)
	r.NoError(err)
	r.NoError(resp.Body.Close())

	r.Equal(secondUser.Email, memberResp.Email)
	r.Equal("user", memberResp.Role)
	r.NotEmpty(memberResp.UID)
}

func TestAddMemberAlreadyMember(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Try to add the test user who is already a member
	addReq := members.AddMemberRequest{
		Email: TestUserEmail,
		Role:  "user",
	}
	reqBody, err := json.Marshal(addReq)
	r.NoError(err)

	resp, err := helper.doRequest(t, "POST", "/api/v1/orgs/"+TestOrgSlug+"/members", reqBody)
	r.NoError(err)
	r.Equal(http.StatusConflict, resp.StatusCode)
	r.NoError(resp.Body.Close())
}

func TestAddMemberInvalidRole(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Create a second user to add
	secondUser := createSecondTestUser(t, testServer)

	// Try to add member with invalid role
	addReq := members.AddMemberRequest{
		Email: secondUser.Email,
		Role:  "superadmin",
	}
	reqBody, err := json.Marshal(addReq)
	r.NoError(err)

	resp, err := helper.doRequest(t, "POST", "/api/v1/orgs/"+TestOrgSlug+"/members", reqBody)
	r.NoError(err)
	r.Equal(http.StatusUnprocessableEntity, resp.StatusCode)
	r.NoError(resp.Body.Close())
}

func TestAddMemberUserNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Try to add non-existent user
	addReq := members.AddMemberRequest{
		Email: "nonexistent@example.com",
		Role:  "user",
	}
	reqBody, err := json.Marshal(addReq)
	r.NoError(err)

	resp, err := helper.doRequest(t, "POST", "/api/v1/orgs/"+TestOrgSlug+"/members", reqBody)
	r.NoError(err)
	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.NoError(resp.Body.Close())
}

func TestGetMember(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// First get list of members to find a UID
	listResp, err := helper.doRequest(t, "GET", "/api/v1/orgs/"+TestOrgSlug+"/members", nil)
	r.NoError(err)
	r.Equal(http.StatusOK, listResp.StatusCode)

	var list listMembersResponse
	err = json.NewDecoder(listResp.Body).Decode(&list)
	r.NoError(err)
	r.NoError(listResp.Body.Close())
	r.NotEmpty(list.Data)

	memberUID := list.Data[0].UID

	// Get specific member
	resp, err := helper.doRequest(t, "GET", "/api/v1/orgs/"+TestOrgSlug+"/members/"+memberUID, nil)
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)

	var member memberResponse
	err = json.NewDecoder(resp.Body).Decode(&member)
	r.NoError(err)
	r.NoError(resp.Body.Close())

	r.Equal(memberUID, member.UID)
	r.Equal(TestUserEmail, member.Email)
}

func TestGetMemberNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Try to get non-existent member
	resp, err := helper.doRequest(t, "GET", "/api/v1/orgs/"+TestOrgSlug+"/members/non-existent-uid", nil)
	r.NoError(err)
	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.NoError(resp.Body.Close())
}

func TestUpdateMemberRole(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Create and add a second user
	secondUser := createSecondTestUser(t, testServer)
	addMemberToOrg(t, helper, secondUser.Email, "user")

	// Find the second user's membership
	memberUID := findMemberUIDByEmail(t, helper, secondUser.Email)

	// Update to viewer role
	newRole := "viewer"
	updateReq := members.UpdateMemberRequest{
		Role: &newRole,
	}
	reqBody, err := json.Marshal(updateReq)
	r.NoError(err)

	resp, err := helper.doRequest(t, "PATCH", "/api/v1/orgs/"+TestOrgSlug+"/members/"+memberUID, reqBody)
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)

	var member memberResponse
	err = json.NewDecoder(resp.Body).Decode(&member)
	r.NoError(err)
	r.NoError(resp.Body.Close())

	r.Equal("viewer", member.Role)
}

func TestCannotDemoteLastAdmin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Find the test user's membership (the only admin)
	memberUID := findMemberUIDByEmail(t, helper, TestUserEmail)

	// Try to demote to user role
	newRole := "user"
	updateReq := members.UpdateMemberRequest{
		Role: &newRole,
	}
	reqBody, err := json.Marshal(updateReq)
	r.NoError(err)

	resp, err := helper.doRequest(t, "PATCH", "/api/v1/orgs/"+TestOrgSlug+"/members/"+memberUID, reqBody)
	r.NoError(err)
	r.Equal(http.StatusConflict, resp.StatusCode)
	r.NoError(resp.Body.Close())
}

func TestRemoveMember(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Create and add a second user
	secondUser := createSecondTestUser(t, testServer)
	addMemberToOrg(t, helper, secondUser.Email, "user")

	// Find the second user's membership
	memberUID := findMemberUIDByEmail(t, helper, secondUser.Email)

	// Remove member
	resp, err := helper.doRequest(t, "DELETE", "/api/v1/orgs/"+TestOrgSlug+"/members/"+memberUID, nil)
	r.NoError(err)
	r.Equal(http.StatusNoContent, resp.StatusCode)
	r.NoError(resp.Body.Close())

	// Verify member is removed
	getResp, err := helper.doRequest(t, "GET", "/api/v1/orgs/"+TestOrgSlug+"/members/"+memberUID, nil)
	r.NoError(err)
	r.Equal(http.StatusNotFound, getResp.StatusCode)
	r.NoError(getResp.Body.Close())
}

func TestCannotRemoveLastAdmin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	helper := newMemberTestHelper(t, testServer)

	// Find the test user's membership (the only admin)
	memberUID := findMemberUIDByEmail(t, helper, TestUserEmail)

	// Try to remove the last admin
	resp, err := helper.doRequest(t, "DELETE", "/api/v1/orgs/"+TestOrgSlug+"/members/"+memberUID, nil)
	r.NoError(err)
	r.Equal(http.StatusConflict, resp.StatusCode)
	r.NoError(resp.Body.Close())
}

func TestMembersUnauthorized(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	testServer := NewTestServer(t)
	ctx := t.Context()

	// Don't login - try to access members without auth
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, testServer.HTTPServer.URL+"/api/v1/orgs/"+TestOrgSlug+"/members", nil)
	r.NoError(err)

	client := &http.Client{}
	resp, err := client.Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusUnauthorized, resp.StatusCode)
}

func TestMembersValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           map[string]any
		expectedStatus int
	}{
		{
			name:           "missing email",
			body:           map[string]any{"role": "user"},
			expectedStatus: http.StatusUnprocessableEntity,
		},
		{
			name:           "missing role",
			body:           map[string]any{"email": "test@example.com"},
			expectedStatus: http.StatusUnprocessableEntity,
		},
		{
			name:           "empty email",
			body:           map[string]any{"email": "", "role": "user"},
			expectedStatus: http.StatusUnprocessableEntity,
		},
		{
			name:           "empty role",
			body:           map[string]any{"email": "test@example.com", "role": ""},
			expectedStatus: http.StatusUnprocessableEntity,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			testServer := NewTestServer(t)
			helper := newMemberTestHelper(t, testServer)

			reqBody, err := json.Marshal(tc.body)
			r.NoError(err)

			resp, err := helper.doRequest(t, "POST", "/api/v1/orgs/"+TestOrgSlug+"/members", reqBody)
			r.NoError(err)
			r.Equal(tc.expectedStatus, resp.StatusCode)
			r.NoError(resp.Body.Close())
		})
	}
}

// Helper functions

func createSecondTestUser(t *testing.T, testServer *TestServer) *models.User {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()
	dbService := testServer.Server.DBService()
	now := time.Now()

	passwordHash, err := passwords.Hash("second-user-password")
	r.NoError(err)

	user := &models.User{
		UID:          "20000000-0000-0000-0000-000000000001",
		Email:        "second@example.com",
		PasswordHash: &passwordHash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	err = dbService.CreateUser(ctx, user)
	r.NoError(err)

	return user
}

func addMemberToOrg(t *testing.T, helper *memberTestHelper, email, role string) {
	t.Helper()

	r := require.New(t)

	addReq := members.AddMemberRequest{
		Email: email,
		Role:  role,
	}
	reqBody, err := json.Marshal(addReq)
	r.NoError(err)

	resp, err := helper.doRequest(t, "POST", "/api/v1/orgs/"+TestOrgSlug+"/members", reqBody)
	r.NoError(err)
	r.Equal(http.StatusCreated, resp.StatusCode)
	r.NoError(resp.Body.Close())
}

func findMemberUIDByEmail(t *testing.T, helper *memberTestHelper, email string) string {
	t.Helper()

	r := require.New(t)

	resp, err := helper.doRequest(t, "GET", "/api/v1/orgs/"+TestOrgSlug+"/members", nil)
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)

	var list listMembersResponse
	err = json.NewDecoder(resp.Body).Decode(&list)
	r.NoError(err)
	r.NoError(resp.Body.Close())

	for _, member := range list.Data {
		if member.Email == email {
			return member.UID
		}
	}

	t.Fatalf("member with email %s not found", email)
	return ""
}
