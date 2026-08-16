package checks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// TestCreateCheckHandlerReturns402OverCap verifies the HTTP layer translates a
// MaxChecks quota breach into 402 with code QUOTA_EXCEEDED and quota fields.
func TestCreateCheckHandlerReturns402OverCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("quota-h", "Quota Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, org.UID, entcore.Entitlements{
		Limits: entcore.Limits{MaxChecks: entcore.Int(1)},
		Source: models.EntitlementSourceAdmin,
	}, "user:test", ""))

	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	router.NewGroup("/api/v1/orgs/:org/checks").POST("", handler.CreateCheck)

	post := func() *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(map[string]any{
			"type":   "http",
			"config": map[string]any{"url": "https://example.com"},
		})
		r.NoError(marshalErr)
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodPost, "/api/v1/orgs/"+org.Slug+"/checks", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		return rec
	}

	// First create fits under the cap of 1.
	rec := post()
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	// Second create exceeds the cap → 402 Payment Required.
	rec = post()
	r.Equal(http.StatusPaymentRequired, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(string(base.ErrorCodeQuotaExceeded), body["code"])
	r.Equal("MaxChecks", body["limitName"])
	r.InDelta(float64(1), body["limit"], 0.0001)
	r.InDelta(float64(1), body["currentUsage"], 0.0001)
}

// newCheckHandlerRouter builds a checks handler over a fresh in-memory db with
// a single org and POST/GET/PATCH routes wired. Returns the router and org slug.
func newCheckHandlerRouter(t *testing.T) (*httpx.Router, string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("flap-h", "Flap Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.POST("", handler.CreateCheck)
	group.GET("/:checkUid", handler.GetCheck)
	group.PATCH("/:checkUid", handler.UpdateCheck)

	return router, org.Slug
}

// TestCreateCheckFlappingFieldsRoundTrip verifies the three flapping
// (adaptive-recovery) knobs are accepted on create and echoed back on GET.
func TestCreateCheckFlappingFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	body, err := json.Marshal(map[string]any{
		"type":                  "http",
		"config":                map[string]any{"url": "https://example.com"},
		"flappingWindowSeconds": 7200,
		"flapBackoffFactor":     3,
		"maxRecoveryMultiplier": 5,
	})
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/orgs/"+orgSlug+"/checks", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	r.InDelta(float64(7200), created["flappingWindowSeconds"], 0.0001)
	r.InDelta(float64(3), created["flapBackoffFactor"], 0.0001)
	r.InDelta(float64(5), created["maxRecoveryMultiplier"], 0.0001)

	uid, _ := created["uid"].(string)
	r.NotEmpty(uid)

	getReq := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/orgs/"+orgSlug+"/checks/"+uid, http.NoBody)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	r.Equal(http.StatusOK, getRec.Code, getRec.Body.String())

	var fetched map[string]any
	r.NoError(json.Unmarshal(getRec.Body.Bytes(), &fetched))
	r.InDelta(float64(7200), fetched["flappingWindowSeconds"], 0.0001)
	r.InDelta(float64(3), fetched["flapBackoffFactor"], 0.0001)
	r.InDelta(float64(5), fetched["maxRecoveryMultiplier"], 0.0001)
}

// TestCreateCheckAutoSlugDoesNotDoubleTypePrefix is a regression test for the
// generated-slug double-prefix bug: creating a domain check for google.com
// without a slug used to yield "domain-domain-google-com" because both the
// checker's Validate and the service layer prepended the type. It must now
// yield "domain-google-com".
func TestCreateCheckAutoSlugDoesNotDoubleTypePrefix(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	rec := postCheck(t, router, orgSlug, map[string]any{
		"type":   "domain",
		"config": map[string]any{"domain": "google.com"},
	})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	r.Equal("domain-google-com", created["slug"])
}

// postCheck marshals body and POSTs it to the org's checks collection.
func postCheck(t *testing.T, router *httpx.Router, orgSlug string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	r := require.New(t)

	payload, err := json.Marshal(body)
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/orgs/"+orgSlug+"/checks", bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestCheckPeriodBoundsOnCreate covers the server-side per-type period floors
// (spec 2026-07-01-04 D1/D2) end to end at the HTTP layer: heavy types get
// their registry MinPeriod, everything else the global 10s floor, and the
// sleep / internal exemptions hold.
func TestCheckPeriodBoundsOnCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        map[string]any
		wantStatus  int
		wantMessage string // substring of the VALIDATION_ERROR title
	}{
		{
			name: "browser at 10s is rejected naming the 60s floor",
			body: map[string]any{
				"type": "browser", "period": "00:00:10",
				"config": map[string]any{"url": "https://example.com"},
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "period for browser checks must be at least 60s",
		},
		{
			name: "browser at 60s is created",
			body: map[string]any{
				"type": "browser", "period": "00:01:00",
				"config": map[string]any{"url": "https://example.com"},
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "js at 10s is rejected naming the 30s floor",
			body: map[string]any{
				"type": "js", "period": "00:00:10",
				"config": map[string]any{"script": "sp.ok()"},
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "period for js checks must be at least 30s",
		},
		{
			name: "plain http at 5s hits the global 10s floor",
			body: map[string]any{
				"type": "http", "period": "PT5S",
				"config": map[string]any{"url": "https://example.com"},
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "period for http checks must be at least 10s",
		},
		{
			name: "plain http at 10s is created",
			body: map[string]any{
				"type": "http", "period": "00:00:10",
				"config": map[string]any{"url": "https://example.com"},
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "sleep at 10s is exempt",
			body: map[string]any{
				"type": "sleep", "period": "00:00:10",
				"config": map[string]any{"sleep_ms": 200},
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "internal browser at 10s is exempt",
			body: map[string]any{
				"type": "browser", "period": "00:00:10", "internal": true,
				"config": map[string]any{"url": "https://example.com"},
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "absent period falls back to the default and passes",
			body: map[string]any{
				"type":   "browser",
				"config": map[string]any{"url": "https://example.com"},
			},
			wantStatus: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			router, orgSlug := newCheckHandlerRouter(t)

			rec := postCheck(t, router, orgSlug, tt.body)
			r.Equal(tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantMessage == "" {
				return
			}

			var errBody map[string]any
			r.NoError(json.Unmarshal(rec.Body.Bytes(), &errBody))
			r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
			r.Contains(errBody["title"], tt.wantMessage)
		})
	}
}

// TestCheckPeriodBoundsOnPatch pins that PATCH re-validates the period against
// the stored type: shrinking below the floor is a 400, a legal value succeeds,
// and a period-less PATCH leaves a grandfathered value untouched.
func TestCheckPeriodBoundsOnPatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	rec := postCheck(t, router, orgSlug, map[string]any{
		"type": "browser", "period": "00:05:00",
		"config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	uid, _ := created["uid"].(string)
	r.NotEmpty(uid)

	patch := func(body map[string]any) *httptest.ResponseRecorder {
		payload, err := json.Marshal(body)
		r.NoError(err)
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodPatch, "/api/v1/orgs/"+orgSlug+"/checks/"+uid, bytes.NewBuffer(payload))
		req.Header.Set("Content-Type", "application/json")
		patchRec := httptest.NewRecorder()
		router.ServeHTTP(patchRec, req)

		return patchRec
	}

	// Shrinking below the browser floor is rejected with the bound in the message.
	patchRec := patch(map[string]any{"period": "00:00:10"})
	r.Equal(http.StatusBadRequest, patchRec.Code, patchRec.Body.String())

	var errBody map[string]any
	r.NoError(json.Unmarshal(patchRec.Body.Bytes(), &errBody))
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
	r.Contains(errBody["title"], "period for browser checks must be at least 60s")

	// A legal period is accepted.
	patchRec = patch(map[string]any{"period": "00:02:00"})
	r.Equal(http.StatusOK, patchRec.Code, patchRec.Body.String())

	// A PATCH without a period never re-validates (grandfathering: the limit
	// bites only on a write to the period).
	patchRec = patch(map[string]any{"name": "renamed"})
	r.Equal(http.StatusOK, patchRec.Code, patchRec.Body.String())
}

// TestCheckDetailSchedulingBlock covers the read-only scheduling telemetry
// (spec 2026-07-01-04 D3): absent before the first run (no cost signal), max
// across the per-region jobs once present, dutyCyclePct = round(100 × cost /
// period), and never present on list responses.
func TestCheckDetailSchedulingBlock(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("sched-h", "Scheduling Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.POST("", handler.CreateCheck)
	group.GET("", handler.ListChecks)
	group.GET("/:checkUid", handler.GetCheck)

	rec := postCheck(t, router, org.Slug, map[string]any{
		"type": "http", "period": "00:00:10",
		"config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	uid, _ := created["uid"].(string)
	r.NotEmpty(uid)

	getDetail := func() map[string]any {
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks/"+uid, http.NoBody)
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, req)
		r.Equal(http.StatusOK, getRec.Code, getRec.Body.String())

		var body map[string]any
		r.NoError(json.Unmarshal(getRec.Body.Bytes(), &body))

		return body
	}

	// Before the first run there is no cost signal → the block is absent.
	r.NotContains(getDetail(), "scheduling", "scheduling must be omitted until the first run")

	// Seed two per-region jobs with diverging EWMAs; detail reports the max.
	regionEU, regionUS := "eu", "us"
	jobEU := models.NewCheckJob(org.UID, uid, timeutils.Duration(10*time.Second))
	jobEU.Region = &regionEU
	jobEU.CostEWMAMs = 2000
	jobEU.DelayEWMAMs = 13432
	r.NoError(dbSvc.CreateCheckJob(ctx, jobEU))

	jobUS := models.NewCheckJob(org.UID, uid, timeutils.Duration(10*time.Second))
	jobUS.Region = &regionUS
	jobUS.CostEWMAMs = 10036
	jobUS.DelayEWMAMs = 500
	r.NoError(dbSvc.CreateCheckJob(ctx, jobUS))

	detail := getDetail()
	sched, ok := detail["scheduling"].(map[string]any)
	r.True(ok, "scheduling block expected on the detail response: %v", detail)
	r.InDelta(float64(10036), sched["costEwmaMs"], 0.0001, "max cost across regions")
	r.InDelta(float64(13432), sched["delayEwmaMs"], 0.0001, "max delay across regions")
	// 10036ms cost on a 10000ms period → round(100.36) = 100.
	r.InDelta(float64(100), sched["dutyCyclePct"], 0.0001)

	// List responses stay lean: no scheduling key on any item.
	listReq := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks", http.NoBody)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	r.Equal(http.StatusOK, listRec.Code, listRec.Body.String())

	var list struct {
		Data []map[string]any `json:"data"`
	}
	r.NoError(json.Unmarshal(listRec.Body.Bytes(), &list))
	r.NotEmpty(list.Data)
	for _, item := range list.Data {
		r.NotContains(item, "scheduling", "list responses must not carry the scheduling block")
	}
}

// TestCreateCheckFlappingValidationRejectsBadFactor pins that an out-of-range
// flapBackoffFactor (< 1) is a 400 VALIDATION_ERROR, not a 500.
func TestCreateCheckFlappingValidationRejectsBadFactor(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	body, err := json.Marshal(map[string]any{
		"type":              "http",
		"config":            map[string]any{"url": "https://example.com"},
		"flapBackoffFactor": 0, // invalid: must be >= 1
	})
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/v1/orgs/"+orgSlug+"/checks", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	var errBody map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &errBody))
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
}

// TestCheckConfigTimeoutCapOnCreate covers the uniform per-check timeout range
// (spec 2026-07-11-05 + the explicit 1s floor from spec 2026-07-11-09) at the
// HTTP layer on create: values in [1s, 30s] pass, anything else (including
// sub-second values) is a 422 VALIDATION_ERROR on the `timeout` field,
// uniformly across check types (including ones whose checker has no own
// timeout cap).
func TestCheckConfigTimeoutCapOnCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantMsg    string // substring of the timeout field error
	}{
		{
			name: "http with 10s timeout is created",
			body: map[string]any{
				"type":   "http",
				"config": map[string]any{"url": "https://example.com", "timeout": "10s"},
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "30s boundary is accepted",
			body: map[string]any{
				"type":   "tcp",
				"config": map[string]any{"host": "example.com", "port": 443, "timeout": "30s"},
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "1s floor is accepted",
			body: map[string]any{
				"type":   "tcp",
				"config": map[string]any{"host": "example.com", "port": 443, "timeout": "1s"},
			},
			wantStatus: http.StatusCreated,
		},
		{
			name: "31s is rejected",
			body: map[string]any{
				"type":   "tcp",
				"config": map[string]any{"host": "example.com", "port": 443, "timeout": "31s"},
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "must be >= 1s and <= 30s",
		},
		{
			name: "sub-second is rejected by the 1s floor",
			body: map[string]any{
				"type":   "http",
				"config": map[string]any{"url": "https://example.com", "timeout": "500ms"},
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "must be >= 1s and <= 30s",
		},
		{
			name: "zero is rejected",
			body: map[string]any{
				"type":   "http",
				"config": map[string]any{"url": "https://example.com", "timeout": "0s"},
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "must be >= 1s and <= 30s",
		},
		{
			name: "negative is rejected",
			body: map[string]any{
				"type":   "http",
				"config": map[string]any{"url": "https://example.com", "timeout": "-5s"},
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "must be >= 1s and <= 30s",
		},
		{
			name: "udp (checker without its own 30s cap) is capped too",
			body: map[string]any{
				"type":   "udp",
				"config": map[string]any{"host": "example.com", "port": 53, "timeout": "45s"},
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "must be >= 1s and <= 30s",
		},
		{
			name: "absent timeout is accepted",
			body: map[string]any{
				"type":   "http",
				"config": map[string]any{"url": "https://example.com"},
			},
			wantStatus: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			router, orgSlug := newCheckHandlerRouter(t)

			rec := postCheck(t, router, orgSlug, tt.body)
			r.Equal(tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantMsg == "" {
				return
			}

			var errBody map[string]any
			r.NoError(json.Unmarshal(rec.Body.Bytes(), &errBody))
			r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
			fields, _ := errBody["fields"].([]any)
			r.Len(fields, 1)
			field, _ := fields[0].(map[string]any)
			r.Equal("timeout", field["name"])
			r.Contains(field["message"], tt.wantMsg)
		})
	}
}

// TestCheckConfigTimeoutCapOnPatch pins the PATCH side of the uniform timeout
// cap (spec 2026-07-11-05): an over-cap value is a 422 VALIDATION_ERROR, a
// legal value persists into config, and a subsequent config without the key
// removes it (non-secret keys are replace-wholesale on PATCH).
func TestCheckConfigTimeoutCapOnPatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	rec := postCheck(t, router, orgSlug, map[string]any{
		"type": "http", "config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	uid, _ := created["uid"].(string)
	r.NotEmpty(uid)

	patch := func(body map[string]any) *httptest.ResponseRecorder {
		payload, err := json.Marshal(body)
		r.NoError(err)
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodPatch, "/api/v1/orgs/"+orgSlug+"/checks/"+uid, bytes.NewBuffer(payload))
		req.Header.Set("Content-Type", "application/json")
		patchRec := httptest.NewRecorder()
		router.ServeHTTP(patchRec, req)

		return patchRec
	}

	getConfig := func() map[string]any {
		getReq := httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, "/api/v1/orgs/"+orgSlug+"/checks/"+uid, http.NoBody)
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)
		r.Equal(http.StatusOK, getRec.Code, getRec.Body.String())

		var fetched map[string]any
		r.NoError(json.Unmarshal(getRec.Body.Bytes(), &fetched))
		cfg, _ := fetched["config"].(map[string]any)
		r.NotNil(cfg)

		return cfg
	}

	// Over-cap timeout on PATCH is rejected as a field validation error.
	patchRec := patch(map[string]any{
		"config": map[string]any{"url": "https://example.com", "timeout": "31s"},
	})
	r.Equal(http.StatusUnprocessableEntity, patchRec.Code, patchRec.Body.String())

	var errBody map[string]any
	r.NoError(json.Unmarshal(patchRec.Body.Bytes(), &errBody))
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
	fields, _ := errBody["fields"].([]any)
	r.Len(fields, 1)
	field, _ := fields[0].(map[string]any)
	r.Equal("timeout", field["name"])
	r.Contains(field["message"], "must be >= 1s and <= 30s")

	// A legal value persists into config.
	patchRec = patch(map[string]any{
		"config": map[string]any{"url": "https://example.com", "timeout": "10s"},
	})
	r.Equal(http.StatusOK, patchRec.Code, patchRec.Body.String())
	r.Equal("10s", getConfig()["timeout"])

	// A config without the key removes it (clearing the form field).
	patchRec = patch(map[string]any{
		"config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusOK, patchRec.Code, patchRec.Body.String())
	r.NotContains(getConfig(), "timeout")
}

// TestLastResultListVsDetailShape is the regression test for spec
// 2026-07-06-01 Part B: with=last_result on the list endpoint must return a
// slim lastResult ({uid, status, timestamp, durationMs}), while the same
// query param on the detail endpoint must keep the full object (output,
// metrics) that the detail page renders (Output/Metrics/SSL-chain card).
func TestLastResultListVsDetailShape(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("lastres-h", "Last Result Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.POST("", handler.CreateCheck)
	group.GET("", handler.ListChecks)
	group.GET("/:checkUid", handler.GetCheck)

	rec := postCheck(t, router, org.Slug, map[string]any{
		"type": "http", "config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	uid, _ := created["uid"].(string)
	r.NotEmpty(uid)

	// Seed a raw result carrying Output/Metrics — the fields Part B must
	// strip from list responses but keep on detail responses.
	result := models.NewResult(org.UID, uid, models.ResultStatusUp, 123.45)
	result.Output = models.JSONMap{"message": "OK", "sslChain": []string{"leaf", "intermediate", "root"}}
	result.Metrics = models.JSONMap{"dnsblHits": 0}
	r.NoError(dbSvc.CreateResult(ctx, result))

	// --- List: ?with=last_result must be slim ---
	listReq := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks?with=last_result", http.NoBody)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	r.Equal(http.StatusOK, listRec.Code, listRec.Body.String())

	var list struct {
		Data []map[string]any `json:"data"`
	}
	r.NoError(json.Unmarshal(listRec.Body.Bytes(), &list))
	r.NotEmpty(list.Data)

	lastResult, ok := list.Data[0]["lastResult"].(map[string]any)
	r.True(ok, "lastResult expected on list item: %v", list.Data[0])
	r.Contains(lastResult, "uid")
	r.Contains(lastResult, "status")
	r.Contains(lastResult, "timestamp")
	r.Contains(lastResult, "durationMs")
	r.NotContains(lastResult, "output", "list lastResult must not carry output")
	r.NotContains(lastResult, "metrics", "list lastResult must not carry metrics")
	r.Equal("up", lastResult["status"])
	r.InDelta(float64(123.45), lastResult["durationMs"], 0.001)

	// --- Detail: ?with=last_result must stay full ---
	detailReq := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/orgs/"+org.Slug+"/checks/"+uid+"?with=last_result", http.NoBody)
	detailRec := httptest.NewRecorder()
	router.ServeHTTP(detailRec, detailReq)
	r.Equal(http.StatusOK, detailRec.Code, detailRec.Body.String())

	var detail map[string]any
	r.NoError(json.Unmarshal(detailRec.Body.Bytes(), &detail))

	detailLastResult, ok := detail["lastResult"].(map[string]any)
	r.True(ok, "lastResult expected on detail response: %v", detail)
	r.Contains(detailLastResult, "output", "detail lastResult must keep output")
	r.Contains(detailLastResult, "metrics", "detail lastResult must keep metrics")

	detailOutput, ok := detailLastResult["output"].(map[string]any)
	r.True(ok, "output expected to be an object: %v", detailLastResult)
	r.Equal("OK", detailOutput["message"])

	detailMetrics, ok := detailLastResult["metrics"].(map[string]any)
	r.True(ok, "metrics expected to be an object: %v", detailLastResult)
	r.InDelta(float64(0), detailMetrics["dnsblHits"], 0.0001)
}
