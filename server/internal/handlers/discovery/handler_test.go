package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/discovery"
)

// newRouter builds an HTTP router wired to the fixture's discovery handler.
func (f *discoveryFixture) newRouter(t *testing.T) *bunrouter.Router {
	t.Helper()

	handler := discovery.NewHandler(f.svc, &config.Config{})

	router := bunrouter.New()
	group := router.NewGroup("/api/v1/orgs/:org/discovery")
	handler.RegisterRoutes(group)

	return router
}

// doPromote issues a POST .../hosts/:uid/promote with admin claims + org context.
func (f *discoveryFixture) doPromote(
	t *testing.T, router *bunrouter.Router, hostUID string, body any,
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

	path := "/api/v1/orgs/" + f.org.Slug + "/discovery/hosts/" + hostUID + "/promote"
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewBuffer(bodyBytes))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

func TestHandlerPromoteMultiCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	hostUID := f.insertPromotableHost(t, "10.0.0.1", []disc.SuggestedCheck{
		{Type: "http", Config: map[string]any{"url": "http://10.0.0.1"}},
		{Type: "ping", Config: map[string]any{"host": "10.0.0.1"}},
	})

	rec := f.doPromote(t, router, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{
			{CheckType: "http", Name: "h1 (http)", Slug: "host-one-http"},
			{CheckType: "ping", Name: "h1 (ping)", Slug: "host-one-ping"},
		},
	})
	r.Equal(http.StatusCreated, rec.Code)

	var resp struct {
		Data []map[string]any `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Len(resp.Data, 2)
}

func TestHandlerPromoteEmptyChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	hostUID := f.insertPromotableHost(t, "10.0.0.2", nil)

	rec := f.doPromote(t, router, hostUID, discovery.PromoteRequest{Checks: nil})
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}

func TestHandlerPromoteMissingCheckType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	hostUID := f.insertPromotableHost(t, "10.0.0.3", nil)

	rec := f.doPromote(t, router, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{{CheckType: "", Slug: "no-type"}},
	})
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
}

func TestHandlerPromoteAlreadyPromoted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	hostUID := f.insertPromotableHost(t, "10.0.0.4", []disc.SuggestedCheck{
		{Type: "tcp", Config: map[string]any{"host": "10.0.0.4", "port": 22}},
	})

	rec := f.doPromote(t, router, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{{CheckType: "tcp", Slug: "host-four"}},
	})
	r.Equal(http.StatusCreated, rec.Code)

	rec = f.doPromote(t, router, hostUID, discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{{CheckType: "tcp", Slug: "host-four-again"}},
	})
	r.Equal(http.StatusConflict, rec.Code)
}

func TestHandlerPromoteHostNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	rec := f.doPromote(t, router, "00000000-0000-0000-0000-000000000000", discovery.PromoteRequest{
		Checks: []discovery.PromoteCheckSpec{{CheckType: "tcp", Slug: "ghost-check"}},
	})
	r.Equal(http.StatusNotFound, rec.Code)
}

// doAdminRequest issues a request to a discovery path with admin claims + org context.
func (f *discoveryFixture) doAdminRequest(
	t *testing.T, router *bunrouter.Router, method, path string, body any,
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

func TestHandlerStartScanCreatesPlan(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	path := "/api/v1/orgs/" + f.org.Slug + "/discovery/scans"
	rec := f.doAdminRequest(t, router, http.MethodPost, path, disc.Config{CIDRs: []string{"10.0.0.0/18"}})
	r.Equal(http.StatusCreated, rec.Code)

	var resp struct {
		Data struct {
			UID  string `json:"uid"`
			Type string `json:"type"`
		} `json:"data"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("network_discovery_plan", resp.Data.Type)
	r.NotEmpty(resp.Data.UID)
}

func TestHandlerStartScanLargeRangeStillRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	path := "/api/v1/orgs/" + f.org.Slug + "/discovery/scans"
	rec := f.doAdminRequest(t, router, http.MethodPost, path, disc.Config{CIDRs: []string{"0.0.0.0/7"}})
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "DISCOVERY_RANGE_TOO_LARGE")
}

func TestHandlerGetScanReturnsProgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	// Create a plan scan, then read it back: response must carry a progress block.
	startPath := "/api/v1/orgs/" + f.org.Slug + "/discovery/scans"
	startRec := f.doAdminRequest(t, router, http.MethodPost, startPath, disc.Config{CIDRs: []string{"10.0.0.0/24"}})
	r.Equal(http.StatusCreated, startRec.Code)

	var start struct {
		Data struct {
			UID string `json:"uid"`
		} `json:"data"`
	}
	r.NoError(json.Unmarshal(startRec.Body.Bytes(), &start))

	getPath := startPath + "/" + start.Data.UID
	getRec := f.doAdminRequest(t, router, http.MethodGet, getPath, nil)
	r.Equal(http.StatusOK, getRec.Code)
	r.Contains(getRec.Body.String(), "progress")
	r.Contains(getRec.Body.String(), "derivedStatus")
}

func TestHandlerCancelScan(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	startPath := "/api/v1/orgs/" + f.org.Slug + "/discovery/scans"
	startRec := f.doAdminRequest(t, router, http.MethodPost, startPath, disc.Config{CIDRs: []string{"10.0.0.0/24"}})
	r.Equal(http.StatusCreated, startRec.Code)

	var start struct {
		Data struct {
			UID string `json:"uid"`
		} `json:"data"`
	}
	r.NoError(json.Unmarshal(startRec.Body.Bytes(), &start))

	cancelPath := startPath + "/" + start.Data.UID + "/cancel"
	cancelRec := f.doAdminRequest(t, router, http.MethodPost, cancelPath, nil)
	r.Equal(http.StatusNoContent, cancelRec.Code)
}

func TestHandlerCancelScanNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newDiscoveryFixture(t)
	router := f.newRouter(t)

	cancelPath := "/api/v1/orgs/" + f.org.Slug +
		"/discovery/scans/00000000-0000-0000-0000-000000000000/cancel"
	rec := f.doAdminRequest(t, router, http.MethodPost, cancelPath, nil)
	r.Equal(http.StatusNotFound, rec.Code)
}
