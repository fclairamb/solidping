package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const vendorSummary = `{"page":{"name":"Acme"},"status":{"indicator":"none","description":"ok"},"components":[` +
	`{"id":"g1","name":"Core","status":"operational","group":true},` +
	`{"id":"c1","name":"API","status":"operational","group_id":"g1"}]}`

func vendorPost(t *testing.T, env *viewerEnv, org, token, target string) (int, string) {
	t.Helper()

	body := `{"url":"` + target + `"}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.ts.URL+"/api/v1/orgs/"+org+"/checks/vendor-status/components", strings.NewReader(body))
	require.NoError(t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := env.ts.Client().Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	var out strings.Builder

	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		out.Write(buf[:n])

		if readErr != nil {
			break
		}
	}

	return resp.StatusCode, out.String()
}

func TestVendorStatusComponentsEndpoint(t *testing.T) {
	env := newViewerEnv(t)
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(vendorSummary))
	}))
	t.Cleanup(vendor.Close)

	t.Run("401 without auth", func(t *testing.T) {
		status, _ := vendorPost(t, env, env.org.Slug, "", vendor.URL)
		require.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("403 for a non-member", func(t *testing.T) {
		ctx := context.Background()
		other := models.NewOrganization("othervendor", "Other")
		require.NoError(t, env.server.dbService.CreateOrganization(ctx, other))

		outsider := models.NewUser("outsider@acme.com")
		require.NoError(t, env.server.dbService.CreateUser(ctx, outsider))

		now := time.Now()
		member := models.NewOrganizationMember(other.UID, outsider.UID, models.MemberRoleUser)
		member.JoinedAt = &now
		require.NoError(t, env.server.dbService.CreateOrganizationMember(ctx, member))

		token := mintTestToken(t, env.server, outsider.UID, other.Slug, string(models.MemberRoleUser), false)
		status, _ := vendorPost(t, env, env.org.Slug, token, vendor.URL)
		require.Equal(t, http.StatusForbidden, status)
	})

	t.Run("a private address is refused by the egress guard", func(t *testing.T) {
		env.server.services.EgressGuard = egress.New(false)

		status, body := vendorPost(t, env, env.org.Slug, env.userToken, vendor.URL)
		require.Equal(t, http.StatusBadGateway, status, body)
	})

	t.Run("a non-http url is a validation error", func(t *testing.T) {
		status, _ := vendorPost(t, env, env.org.Slug, env.userToken, "ftp://x.acme.com")
		require.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("lists the components when egress allows it", func(t *testing.T) {
		env.server.services.EgressGuard = egress.New(true)

		status, body := vendorPost(t, env, env.org.Slug, env.userToken, vendor.URL)
		require.Equal(t, http.StatusOK, status, body)

		var out struct {
			Data []struct{ ID, Name, Group string } `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		require.Len(t, out.Data, 1)
		require.Equal(t, "c1", out.Data[0].ID)
		require.Equal(t, "Core", out.Data[0].Group)
	})
}

func TestVendorStatusCatalogEndpointIsPublic(t *testing.T) {
	env := newViewerEnv(t)

	resp, err := env.ts.Client().Get(env.ts.URL + "/api/v1/check-types/vendor-status/catalog")
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Data []map[string]string `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.GreaterOrEqual(t, len(out.Data), 40)
	require.NotEmpty(t, out.Data[0]["slug"])
}
