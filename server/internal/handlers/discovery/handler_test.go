package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/discovery"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// newRouter builds an HTTP router wired to the fixture's discovery handler.
func (f *discoveryFixture) newRouter(t *testing.T) *httpx.Router {
	t.Helper()

	handler := discovery.NewHandler(f.svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/discovery")
	handler.RegisterRoutes(group)

	return router
}

// doAdminRequest issues a request to a discovery path with admin claims + org context.
func (f *discoveryFixture) doAdminRequest(
	t *testing.T, router *httpx.Router, method, path string, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	r := require.New(t)

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		r.NoError(err)
	}

	ctx := context.WithValue(t.Context(), base.ContextKeyClaims, &auth.Claims{
		UserUID: "test-user",
		OrgSlug: f.org.Slug,
		Role:    "admin",
	})
	ctx = context.WithValue(ctx, base.ContextKeyOrganization, f.org)

	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// doUserRequest issues a request with a non-admin (viewer) role.
func (f *discoveryFixture) doUserRequest(
	t *testing.T, router *httpx.Router, method, path string, body any,
) *httptest.ResponseRecorder {
	t.Helper()

	r := require.New(t)

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		r.NoError(err)
	}

	ctx := context.WithValue(t.Context(), base.ContextKeyClaims, &auth.Claims{
		UserUID: "test-user", OrgSlug: f.org.Slug, Role: "viewer",
	})
	ctx = context.WithValue(ctx, base.ContextKeyOrganization, f.org)

	req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func (f *discoveryFixture) scansPath() string {
	return "/api/v1/orgs/" + f.org.Slug + "/discovery/scans"
}

func TestHandlerListTypes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	rec := f.doAdminRequest(t, router, http.MethodGet, "/api/v1/orgs/"+f.org.Slug+"/discovery/types", nil)
	r.Equal(http.StatusOK, rec.Code)

	var resp struct {
		Data []struct {
			Type   string `json:"type"`
			Source string `json:"source"`
		} `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))

	types := map[string]string{}
	for _, d := range resp.Data {
		types[d.Type] = d.Source
	}
	r.Equal("lan", types["lan"])
	r.Equal("freebox", types["freebox"])
	// The kubernetes discovery type appears automatically once registered.
	r.Equal("kubernetes", types["kubernetes"])
}

func TestHandlerStartScanLANCreatesPlan(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	body := discovery.StartScanRequest{Type: "lan", Parameters: json.RawMessage(`{"cidrs":["10.0.0.0/18"]}`)}
	rec := f.doAdminRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusCreated, rec.Code)

	var resp struct {
		Data struct {
			UID  string `json:"uid"`
			Type string `json:"type"`
		} `json:"data"`
		Progress *discovery.ScanProgress `json:"progress"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("network_discovery_plan", resp.Data.Type)
	r.NotEmpty(resp.Data.UID)
	r.NotNil(resp.Progress, "StartScan must return a progress block")
}

func TestHandlerStartScanUnknownType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	body := discovery.StartScanRequest{Type: "bogus", Parameters: json.RawMessage(`{}`)}
	rec := f.doAdminRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Contains(rec.Body.String(), "DISCOVERY_UNKNOWN_TYPE")
}

func TestHandlerStartScanBadParams(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	body := discovery.StartScanRequest{Type: "lan", Parameters: json.RawMessage(`{}`)}
	rec := f.doAdminRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "DISCOVERY_INVALID_PARAMETERS")
}

func TestHandlerStartScanLargeRangeRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	body := discovery.StartScanRequest{Type: "lan", Parameters: json.RawMessage(`{"cidrs":["0.0.0.0/7"]}`)}
	rec := f.doAdminRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "DISCOVERY_RANGE_TOO_LARGE")
}

func TestHandlerStartScanNonAdminForbidden(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	body := discovery.StartScanRequest{Type: "lan", Parameters: json.RawMessage(`{"cidrs":["10.0.0.0/24"]}`)}
	rec := f.doUserRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusForbidden, rec.Code)
}

func TestHandlerStartScanKubernetesUnknownClusterReturns404(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	// An unknown clusterUid does not resolve to a cluster connection in the org,
	// so the kubernetes scantype fails fast with KUBERNETES_CLUSTER_NOT_FOUND →
	// 404 (the spec's guard).
	body := discovery.StartScanRequest{
		Type:       "kubernetes",
		Parameters: json.RawMessage(`{"clusterUid":"00000000-0000-0000-0000-000000000000"}`),
	}
	rec := f.doAdminRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusNotFound, rec.Code)

	var resp struct {
		Code string `json:"code"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("KUBERNETES_CLUSTER_NOT_FOUND", resp.Code)
}

func TestHandlerStartScanKubernetesNonAdminForbidden(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	body := discovery.StartScanRequest{
		Type:       "kubernetes",
		Parameters: json.RawMessage(`{"clusterUid":"any"}`),
	}
	rec := f.doUserRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusForbidden, rec.Code)
}

func TestHandlerGetScanReturnsProgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	body := discovery.StartScanRequest{Type: "lan", Parameters: json.RawMessage(`{"cidrs":["10.0.0.0/24"]}`)}
	startRec := f.doAdminRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusCreated, startRec.Code)

	var start struct {
		Data struct {
			UID string `json:"uid"`
		} `json:"data"`
	}
	r.NoError(json.Unmarshal(startRec.Body.Bytes(), &start))

	getRec := f.doAdminRequest(t, router, http.MethodGet, f.scansPath()+"/"+start.Data.UID, nil)
	r.Equal(http.StatusOK, getRec.Code)
	r.Contains(getRec.Body.String(), "progress")
	r.Contains(getRec.Body.String(), "derivedStatus")
	r.Contains(getRec.Body.String(), "groupCount")
	r.Contains(getRec.Body.String(), "checkCount")
}

func TestHandlerCancelScan(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	body := discovery.StartScanRequest{Type: "lan", Parameters: json.RawMessage(`{"cidrs":["10.0.0.0/24"]}`)}
	startRec := f.doAdminRequest(t, router, http.MethodPost, f.scansPath(), body)
	r.Equal(http.StatusCreated, startRec.Code)

	var start struct {
		Data struct {
			UID string `json:"uid"`
		} `json:"data"`
	}
	r.NoError(json.Unmarshal(startRec.Body.Bytes(), &start))

	cancelRec := f.doAdminRequest(t, router, http.MethodPost, f.scansPath()+"/"+start.Data.UID+"/cancel", nil)
	r.Equal(http.StatusNoContent, cancelRec.Code)
}

func TestHandlerCancelScanNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	rec := f.doAdminRequest(t, router, http.MethodPost,
		f.scansPath()+"/00000000-0000-0000-0000-000000000000/cancel", nil)
	r.Equal(http.StatusNotFound, rec.Code)
}

func TestHandlerListChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	jobUID := f.newScanJob(t)
	f.insertCheck(t, jobUID, "10.0.0.1", "x-tcp", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"10.0.0.1","port":22}`))

	rec := f.doAdminRequest(t, router, http.MethodGet,
		"/api/v1/orgs/"+f.org.Slug+"/discovery/checks?jobUid="+jobUID, nil)
	r.Equal(http.StatusOK, rec.Code)

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Data, 1)
	r.Equal("10.0.0.1", resp.Data[0]["groupKey"])
}

func TestHandlerPromoteChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	jobUID := f.newScanJob(t)
	httpUID := f.insertCheck(t, jobUID, "10.0.0.1", "host-one-http", "http",
		models.DiscoverySourceLAN, json.RawMessage(`{"url":"http://10.0.0.1"}`))
	icmpUID := f.insertCheck(t, jobUID, "10.0.0.1", "host-one-icmp", "ping",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"10.0.0.1"}`))

	rec := f.doAdminRequest(t, router, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/discovery/checks/promote",
		discovery.PromoteRequest{UIDs: []string{httpUID, icmpUID}})
	r.Equal(http.StatusCreated, rec.Code)

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Data, 2)
}

func TestHandlerPromoteEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	rec := f.doAdminRequest(t, router, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/discovery/checks/promote", discovery.PromoteRequest{UIDs: nil})
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}

func TestHandlerPromoteNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	rec := f.doAdminRequest(t, router, http.MethodPost,
		"/api/v1/orgs/"+f.org.Slug+"/discovery/checks/promote",
		discovery.PromoteRequest{UIDs: []string{"00000000-0000-0000-0000-000000000000"}})
	r.Equal(http.StatusNotFound, rec.Code)
}

func TestHandlerDismissCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	jobUID := f.newScanJob(t)
	uid := f.insertCheck(t, jobUID, "10.0.0.5", "y-tcp", "tcp",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"10.0.0.5","port":22}`))

	rec := f.doAdminRequest(t, router, http.MethodDelete,
		"/api/v1/orgs/"+f.org.Slug+"/discovery/checks/"+uid, nil)
	r.Equal(http.StatusNoContent, rec.Code)

	// Second delete is a 404.
	rec = f.doAdminRequest(t, router, http.MethodDelete,
		"/api/v1/orgs/"+f.org.Slug+"/discovery/checks/"+uid, nil)
	r.Equal(http.StatusNotFound, rec.Code)
}

func TestHandlerDismissGroup(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	jobUID := f.newScanJob(t)
	f.insertCheck(t, jobUID, "10.0.0.9", "g1", "http",
		models.DiscoverySourceLAN, json.RawMessage(`{"url":"http://10.0.0.9"}`))
	f.insertCheck(t, jobUID, "10.0.0.9", "g2", "ping",
		models.DiscoverySourceLAN, json.RawMessage(`{"host":"10.0.0.9"}`))

	rec := f.doAdminRequest(t, router, http.MethodDelete,
		"/api/v1/orgs/"+f.org.Slug+"/discovery/checks?jobUid="+jobUID+"&group=10.0.0.9", nil)
	r.Equal(http.StatusNoContent, rec.Code)

	remaining, err := f.svc.ListDiscoveredChecks(t.Context(), f.org.UID, &discovery.ListChecksOptions{})
	r.NoError(err)
	r.Empty(remaining)
}

func TestHandlerDismissGroupMissingParam(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	rec := f.doAdminRequest(t, router, http.MethodDelete,
		"/api/v1/orgs/"+f.org.Slug+"/discovery/checks", nil)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}
