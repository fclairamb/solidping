package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// cliCovUserUID matches the primary test user created by testhelper.
const cliCovUserUID = "10000000-0000-0000-0000-000000000002"

// withRegistration enables self-service registration for a test server.
func withRegistration(cfg *config.Config) {
	cfg.Auth.RegistrationEmailPattern = ".*@example.com"
}

// TestCLICoverage_RegisterConfirm exercises register + confirmRegistration end to
// end. The confirmation token is read back from the state-entry store the way
// the confirm handler expects it to have been persisted.
func TestCLICoverage_RegisterConfirm(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServerWithConfig(t, withRegistration)
	ctx := t.Context()

	const (
		newEmail = "cli-register@example.com"
		newPass  = "registerpass123"
		newName  = "CLI Register"
	)

	apiClient := ts.NewClient()

	name := newName
	regResp, err := apiClient.RegisterWithResponse(ctx, client.RegisterJSONRequestBody{
		Email:    openapi_types.Email(newEmail),
		Password: newPass,
		Name:     &name,
	})
	r.NoError(err)
	r.Equal(200, regResp.StatusCode())
	r.NotNil(regResp.JSON200)
	r.NotNil(regResp.JSON200.Message)

	// The pending registration is stored at email_registration:<email>; pull the
	// confirmation token from it exactly as the confirm handler will look it up.
	entry, err := ts.Server.DBService().GetStateEntry(ctx, nil, "email_registration:"+newEmail)
	r.NoError(err)
	r.NotNil(entry)
	r.NotNil(entry.Value)
	token, ok := (*entry.Value)["token"].(string)
	r.True(ok)
	r.NotEmpty(token)

	confirmResp, err := apiClient.ConfirmRegistrationWithResponse(ctx, client.ConfirmRegistrationJSONRequestBody{
		Token: token,
	})
	r.NoError(err)
	r.Equal(200, confirmResp.StatusCode())
	r.NotNil(confirmResp.JSON200)
	r.NotNil(confirmResp.JSON200.User)
	r.NotNil(confirmResp.JSON200.User.Email)
	r.Equal(newEmail, string(*confirmResp.JSON200.User.Email))

	// The user is now persisted.
	user, err := ts.Server.DBService().GetUserByEmail(ctx, newEmail)
	r.NoError(err)
	r.NotNil(user)
	r.Equal(newName, user.Name)
}

// TestCLICoverage_PasswordReset covers requestPasswordReset (existing user) and
// resetPassword (token seeded the way the request flow persists it).
func TestCLICoverage_PasswordReset(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := ts.NewClient()

	reqResp, err := apiClient.RequestPasswordResetWithResponse(ctx, client.RequestPasswordResetJSONRequestBody{
		Email: openapi_types.Email(TestUserEmail),
	})
	r.NoError(err)
	r.Equal(200, reqResp.StatusCode())
	r.NotNil(reqResp.JSON200)
	r.NotNil(reqResp.JSON200.Message)

	// The request flow stores password_reset:<sha256(token)> -> {userUid}. Seed
	// one directly so we can drive resetPassword with a known plaintext token.
	const resetToken = "cli-reset-token-abcdef0123456789"
	sum := sha256.Sum256([]byte(resetToken))
	stateKey := "password_reset:" + hex.EncodeToString(sum[:])
	ttl := time.Hour
	r.NoError(ts.Server.DBService().SetStateEntry(ctx, nil, stateKey,
		&models.JSONMap{"userUid": cliCovUserUID}, &ttl))

	const newPassword = "cli-reset-newpass123"
	resetResp, err := apiClient.ResetPasswordWithResponse(ctx, client.ResetPasswordJSONRequestBody{
		Token:    resetToken,
		Password: newPassword,
	})
	r.NoError(err)
	r.Equal(200, resetResp.StatusCode())
	r.NotNil(resetResp.JSON200)
	r.NotNil(resetResp.JSON200.Message)

	// The new password must now authenticate.
	loginClient := ts.NewClient()
	_, err = loginClient.Login(ctx, TestOrgSlug, TestUserEmail, newPassword)
	r.NoError(err)
}

// TestCLICoverage_UpdateMe covers the updateMe (PATCH /auth/me) operation.
func TestCLICoverage_UpdateMe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	const newName = "CLI Renamed User"
	name := newName
	resp, err := apiClient.UpdateMeWithResponse(ctx, client.UpdateMeJSONRequestBody{Name: &name})
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.NotNil(resp.JSON200.User)
	r.NotNil(resp.JSON200.User.Name)
	r.Equal(newName, *resp.JSON200.User.Name)

	// The rename is persisted.
	user, err := ts.Server.DBService().GetUserByEmail(ctx, TestUserEmail)
	r.NoError(err)
	r.NotNil(user)
	r.Equal(newName, user.Name)
}

// TestCLICoverage_AuthProviders covers the listAuthProviders operation.
func TestCLICoverage_AuthProviders(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServerWithConfig(t, withRegistration)
	ctx := t.Context()

	apiClient := ts.NewClient()

	resp, err := apiClient.ListAuthProvidersWithResponse(ctx)
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.NotNil(resp.JSON200.Data)
	r.NotNil(resp.JSON200.RegistrationEnabled)
	r.True(*resp.JSON200.RegistrationEnabled)
	r.NotNil(resp.JSON200.PasskeysEnabled)
}

// TestCLICoverage_InviteAcceptInvite covers getInvite + acceptInvite. The
// invitation is created through the real create-invitation handler.
func TestCLICoverage_InviteAcceptInvite(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	adminClient := cliCovAuthedClient(ctx, t, ts)

	const inviteEmail = "cli-invitee@example.com"
	createResp, err := adminClient.CreateInvitationWithResponse(ctx, TestOrgSlug,
		client.CreateInvitationJSONRequestBody{Email: openapi_types.Email(inviteEmail)})
	r.NoError(err)
	r.Equal(201, createResp.StatusCode())
	r.NotNil(createResp.JSON201)
	inviteToken := createResp.JSON201.Token
	r.NotEmpty(inviteToken)

	// Public invite lookup.
	publicClient := ts.NewClient()
	infoResp, err := publicClient.GetInviteWithResponse(ctx, inviteToken)
	r.NoError(err)
	r.Equal(200, infoResp.StatusCode())
	r.NotNil(infoResp.JSON200)
	r.NotNil(infoResp.JSON200.OrgSlug)
	r.Equal(TestOrgSlug, *infoResp.JSON200.OrgSlug)
	r.NotNil(infoResp.JSON200.Role)

	// Accept the invite as a brand-new user.
	const (
		inviteeName = "CLI Invitee"
		inviteePass = "cli-invitee-pass123"
	)
	name := inviteeName
	password := inviteePass
	acceptResp, err := publicClient.AcceptInviteWithResponse(ctx, client.AcceptInviteJSONRequestBody{
		Token:    inviteToken,
		Name:     &name,
		Password: &password,
	})
	r.NoError(err)
	r.Equal(200, acceptResp.StatusCode())
	r.NotNil(acceptResp.JSON200)
	r.NotNil(acceptResp.JSON200.AccessToken)
	r.NotNil(acceptResp.JSON200.User)
	r.NotNil(acceptResp.JSON200.User.Email)
	r.Equal(inviteEmail, string(*acceptResp.JSON200.User.Email))

	// The invitee is now a member and can authenticate.
	user, err := ts.Server.DBService().GetUserByEmail(ctx, inviteEmail)
	r.NoError(err)
	r.NotNil(user)

	loginClient := ts.NewClient()
	_, err = loginClient.Login(ctx, TestOrgSlug, inviteEmail, inviteePass)
	r.NoError(err)
}
