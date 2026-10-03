package checks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// Check version history at the service and HTTP layers (spec 2026-10-03-06).

func versionsUser(t *testing.T, dbSvc db.Service, email string) (context.Context, *models.User) {
	t.Helper()

	user := models.NewUser(email)
	user.Name = "Alice"
	require.NoError(t, dbSvc.CreateUser(t.Context(), user))

	ctx := context.WithValue(t.Context(), base.ContextKeyClaims, &auth.Claims{UserUID: user.UID})
	ctx = audit.WithUser(ctx, user.UID, models.ActorTypeUser)

	return ctx, user
}

func createSSHCheck(t *testing.T, ctx context.Context, svc *checks.Service, orgSlug string) checks.CheckResponse {
	t.Helper()

	created, err := svc.CreateCheck(ctx, orgSlug, checks.CreateCheckRequest{
		Name: "ssh-versions",
		Slug: "ssh-versions",
		Type: "ssh",
		Config: map[string]any{
			"host":     "a.acme.com",
			"username": "alice",
			"password": "first-secret",
		},
	})
	require.NoError(t, err)

	return created
}

func TestCheckVersionsRecordTheCaller(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupPlaintextChecksService(t)
	ctx, user := versionsUser(t, dbSvc, "alice@acme.com")

	created := createSSHCheck(t, ctx, svc, org.Slug)

	name := "ssh renamed"
	_, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)

	list, err := svc.ListCheckVersions(ctx, org.Slug, created.UID, 0)
	r.NoError(err)
	r.Len(list.Data, 2)

	for _, version := range list.Data {
		r.Equal(string(models.CheckVersionOriginUser), version.Origin)
		r.NotNil(version.ActorUserUID)
		r.Equal(user.UID, *version.ActorUserUID)
		r.NotNil(version.ActorName)
		r.Equal("Alice", *version.ActorName)
		r.Nil(version.Snapshot, "the list carries no snapshot")
	}

	// An API token is recorded as `api`.
	tokenCtx := audit.WithUser(ctx, user.UID, models.ActorTypeAPIToken)
	description := "via token"
	_, err = svc.UpdateCheck(tokenCtx, org.Slug, created.UID, &checks.UpdateCheckRequest{Description: &description})
	r.NoError(err)

	list, err = svc.ListCheckVersions(ctx, org.Slug, created.UID, 1)
	r.NoError(err)
	r.Len(list.Data, 1, "limit is honored")
	r.Equal(string(models.CheckVersionOriginAPI), list.Data[0].Origin)

	// The snapshot never carries the secret, and the diff shows the rename.
	v2, err := svc.GetCheckVersion(ctx, org.Slug, created.UID, 2)
	r.NoError(err)
	r.NotNil(v2.Snapshot)
	r.NotContains(v2.Snapshot.Config, "password")
	r.Equal("a.acme.com", v2.Snapshot.Config["host"])

	diff, err := svc.DiffCheckVersion(ctx, org.Slug, created.UID, 2, nil)
	r.NoError(err)
	r.NotNil(diff.Against)
	r.Equal(1, *diff.Against)
	r.Equal([]checks.CheckFieldChange{{Field: "name", From: "ssh-versions", To: "ssh renamed"}}, diff.Changes)

	first, err := svc.DiffCheckVersion(ctx, org.Slug, created.UID, 1, nil)
	r.NoError(err)
	r.Nil(first.Against)
	r.NotEmpty(first.Changes)
}

func TestCheckVersionsSystemWriteWithoutCaller(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupPlaintextChecksService(t)

	created := createSSHCheck(t, t.Context(), svc, org.Slug)

	latest, err := dbSvc.GetLatestAppliedCheckVersion(t.Context(), created.UID)
	r.NoError(err)
	r.Equal(models.CheckVersionOriginSystem, latest.Origin)
	r.Nil(latest.ActorUserUID)
}

func TestRestoreCheckVersionKeepsSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupPlaintextChecksService(t)
	ctx, user := versionsUser(t, dbSvc, "bob@acme.com")

	created := createSSHCheck(t, ctx, svc, org.Slug)

	config := map[string]any{"host": "b.acme.com", "username": "bob", "password": "second-secret"}
	labels := map[string]string{"env": "prod"}
	_, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &config, Labels: &labels})
	r.NoError(err)

	list, err := svc.ListCheckVersions(ctx, org.Slug, created.UID, 0)
	r.NoError(err)
	r.Len(list.Data, 2, "config and labels in one PATCH are one version")

	restored, err := svc.RestoreCheckVersion(ctx, org.Slug, created.UID, 1)
	r.NoError(err)
	r.Equal("a.acme.com", restored.Config["host"])
	r.Equal("alice", restored.Config["username"])
	r.Empty(restored.Labels)

	// The current secret survives: secrets are not versioned.
	row, err := dbSvc.GetCheck(t.Context(), org.UID, created.UID)
	r.NoError(err)
	r.NotNil(row.ConfigPrivate)

	secrets, err := creds.DecryptForOrg(t.Context(), org.UID, *row.ConfigPrivate)
	r.NoError(err)
	r.Equal("second-secret", secrets["password"])

	latest, err := dbSvc.GetLatestAppliedCheckVersion(t.Context(), created.UID)
	r.NoError(err)
	r.Equal(3, latest.Version)
	r.Equal(models.CheckVersionOriginUser, latest.Origin)
	r.Equal(user.UID, *latest.ActorUserUID)
	r.Equal("restored v1", *latest.Reason)

	// Restoring the version that is already live records nothing new.
	_, err = svc.RestoreCheckVersion(ctx, org.Slug, created.UID, 3)
	r.NoError(err)

	latest, err = dbSvc.GetLatestAppliedCheckVersion(t.Context(), created.UID)
	r.NoError(err)
	r.Equal(3, latest.Version)

	_, err = svc.RestoreCheckVersion(ctx, org.Slug, created.UID, 99)
	r.ErrorIs(err, checks.ErrCheckVersionNotFound)
}

func proposeRename(
	t *testing.T, dbSvc db.Service, checkUID, name string, base int,
) *models.CheckVersion {
	t.Helper()

	latest, err := dbSvc.GetLatestAppliedCheckVersion(t.Context(), checkUID)
	require.NoError(t, err)

	snapshot := models.JSONMap{}
	for key, value := range latest.Snapshot {
		snapshot[key] = value
	}

	snapshot["name"] = name

	row := models.NewCheckVersion(latest.OrganizationUID, checkUID, snapshot, "")
	row.BaseVersion = &base
	row.Origin = models.CheckVersionOriginAIRepair
	require.NoError(t, dbSvc.CreateCheckVersionProposal(t.Context(), row))

	return row
}

func TestApproveAndRejectProposals(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupPlaintextChecksService(t)
	ctx, user := versionsUser(t, dbSvc, "carol@acme.com")

	created := createSSHCheck(t, ctx, svc, org.Slug)

	// Reject leaves the check unchanged.
	rejected := proposeRename(t, dbSvc, created.UID, "rejected name", 1)

	response, err := svc.RejectCheckVersion(ctx, org.Slug, created.UID, rejected.Version)
	r.NoError(err)
	r.Equal(string(models.CheckVersionStatusRejected), response.Status)
	r.Equal(user.UID, *response.DecidedByUserUID)

	current, err := svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.Equal("ssh-versions", *current.Name)

	_, err = svc.RejectCheckVersion(ctx, org.Slug, created.UID, rejected.Version)
	r.ErrorIs(err, checks.ErrCheckVersionNotProposed)

	_, err = svc.RestoreCheckVersion(ctx, org.Slug, created.UID, rejected.Version)
	r.ErrorIs(err, checks.ErrCheckVersionNotApplied)

	// Approve applies it, and the proposal row becomes the applied version.
	approved := proposeRename(t, dbSvc, created.UID, "approved name", 1)

	updated, err := svc.ApproveCheckVersion(ctx, org.Slug, created.UID, approved.Version)
	r.NoError(err)
	r.Equal("approved name", *updated.Name)

	row, err := dbSvc.GetCheckVersion(t.Context(), created.UID, approved.Version)
	r.NoError(err)
	r.Equal(models.CheckVersionStatusApplied, row.Status)
	r.Equal(models.CheckVersionOriginAIRepair, row.Origin)
	r.Equal(user.UID, *row.DecidedByUID)

	latest, err := dbSvc.GetLatestAppliedCheckVersion(t.Context(), created.UID)
	r.NoError(err)
	r.Equal(approved.Version, latest.Version, "approving records no extra version")

	// A proposal built on v1 is stale now that the approved one is live.
	stale := proposeRename(t, dbSvc, created.UID, "stale name", 1)

	_, err = svc.ApproveCheckVersion(ctx, org.Slug, created.UID, stale.Version)
	r.ErrorIs(err, checks.ErrCheckVersionStale)

	_, err = svc.ApproveCheckVersion(ctx, org.Slug, created.UID, approved.Version)
	r.ErrorIs(err, checks.ErrCheckVersionNotProposed)
}

func TestCheckVersionsHTTP(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupPlaintextChecksService(t)
	ctx, _ := versionsUser(t, dbSvc, "dave@acme.com")

	created := createSSHCheck(t, ctx, svc, org.Slug)

	other := models.NewOrganization("versions-other", "Other Org")
	r.NoError(dbSvc.CreateOrganization(t.Context(), other))

	handler := checks.NewHandler(svc, &config.Config{})
	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.GET("/:checkUid/versions", handler.ListCheckVersions)
	group.GET("/:checkUid/versions/:version", handler.GetCheckVersion)
	group.GET("/:checkUid/versions/:version/diff", handler.DiffCheckVersion)
	group.POST("/:checkUid/versions/:version/reject", handler.RejectCheckVersion)

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		return rec
	}

	base := "/api/v1/orgs/" + org.Slug + "/checks/" + created.UID + "/versions"

	rec := do(http.MethodGet, base)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var list map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &list))
	r.Contains(list, "data")
	r.Len(list["data"], 1)

	rec = do(http.MethodGet, base+"/1")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.NotContains(rec.Body.String(), "first-secret")
	r.NotContains(rec.Body.String(), "password")

	rec = do(http.MethodGet, base+"/1/diff")
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	rec = do(http.MethodGet, base+"/7")
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())

	rec = do(http.MethodGet, base+"/abc")
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	// Reject on an applied version: 409.
	rec = do(http.MethodPost, base+"/1/reject")
	r.Equal(http.StatusConflict, rec.Code, rec.Body.String())

	// The same check through another org: 404.
	rec = do(http.MethodGet, "/api/v1/orgs/"+other.Slug+"/checks/"+created.UID+"/versions/1")
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())

	rec = do(http.MethodGet, "/api/v1/orgs/"+other.Slug+"/checks/"+created.UID+"/versions")
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
}
