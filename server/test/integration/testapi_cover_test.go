package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func newTestModeServer(t *testing.T) *TestServer {
	t.Helper()

	return NewTestServerWithConfig(t, func(cfg *config.Config) { cfg.RunMode = "test" })
}

//nolint:paralleltest // "conflict" depends on "created" having run first
func TestTestAPICreateUser(t *testing.T) {
	ts := newTestModeServer(t)

	tests := []struct {
		name string
		body any
		want int
	}{
		{"bad body", "not an object", http.StatusBadRequest},
		{"missing email", map[string]any{"password": "x"}, http.StatusBadRequest},
		{"missing password", map[string]any{"email": "a@acme.com"}, http.StatusBadRequest},
		{"created", map[string]any{
			"email": "new@acme.com", "password": "pw", "name": "New", "mustChangePassword": true,
		}, http.StatusCreated},
		{"conflict", map[string]any{"email": "new@acme.com", "password": "pw"}, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, raw := doBearerRequest(t, ts, "", http.MethodPost, "/api/v1/test/users", tt.body)
			require.Equal(t, tt.want, status, string(raw))

			if tt.want == http.StatusCreated {
				var got struct {
					Email              string `json:"email"`
					MustChangePassword bool   `json:"mustChangePassword"`
				}
				require.NoError(t, json.Unmarshal(raw, &got))
				require.Equal(t, "new@acme.com", got.Email)
				require.True(t, got.MustChangePassword)
			}
		})
	}
}

//nolint:paralleltest // subtests share one server and its generated data
func TestTestAPIGenerateData(t *testing.T) {
	ts := newTestModeServer(t)

	tests := []struct {
		name string
		body any
		want int
	}{
		{"bad body", "nope", http.StatusBadRequest},
		{"missing start date", map[string]any{"org": TestOrgSlug}, http.StatusBadRequest},
		{"bad start date", map[string]any{"org": TestOrgSlug, "startDate": "yesterday"}, http.StatusBadRequest},
		{"unknown org", map[string]any{"org": "nope", "startDate": "2026-01-01"}, http.StatusBadRequest},
		{"ok date only", map[string]any{"org": TestOrgSlug, "startDate": "2099-01-01"}, http.StatusOK},
		{"ok rfc3339 with failures", map[string]any{
			"org": TestOrgSlug, "startDate": "2099-01-01T00:00:00Z", "checkPeriodSec": 3600,
			"failureRate": 0.5, "failureBurstSec": 7200, "name": "gen",
		}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, raw := doBearerRequest(t, ts, "", http.MethodPost, "/api/v1/test/generate-data", tt.body)
			require.Equal(t, tt.want, status, string(raw))

			if tt.want == http.StatusOK {
				var got struct {
					CheckUID     string `json:"checkUid"`
					ResultsCount int    `json:"resultsCount"`
				}
				require.NoError(t, json.Unmarshal(raw, &got))
				require.NotEmpty(t, got.CheckUID)
			}
		})
	}
}

func TestTestAPIBulkChecks(t *testing.T) {
	t.Parallel()

	ts := newTestModeServer(t)

	bulk := func(method string, q url.Values) (int, []byte) {
		return doBearerRequest(t, ts, "", method, "/api/v1/test/checks/bulk?"+q.Encode(), nil)
	}
	base := func() url.Values {
		return url.Values{
			"type": {"http"}, "slug": {"bulk-{nb}"}, "count": {"3"}, "org": {TestOrgSlug},
			"url": {"https://acme.com/{nb}"},
		}
	}

	// Parameter validation.
	for name, mutate := range map[string]func(url.Values){
		"no type":        func(q url.Values) { q.Del("type") },
		"no slug":        func(q url.Values) { q.Del("slug") },
		"no placeholder": func(q url.Values) { q.Set("slug", "bulk") },
		"no count":       func(q url.Values) { q.Del("count") },
		"count too big":  func(q url.Values) { q.Set("count", "999999") },
		"bad period":     func(q url.Values) { q.Set("period", "soon") },
		"unknown org":    func(q url.Values) { q.Set("org", "nope") },
		"unknown type":   func(q url.Values) { q.Set("type", "nope") },
	} {
		q := base()
		mutate(q)
		status, raw := bulk(http.MethodPost, q)
		require.Equal(t, http.StatusBadRequest, status, name+": "+string(raw))
	}

	// Validation failures are reported per check, not as an HTTP error.
	q := base()
	q.Del("url")
	q.Set("slug", "bad-{nb}")
	status, raw := bulk(http.MethodPost, q)
	require.Equal(t, http.StatusOK, status, string(raw))
	var failed BulkResp
	require.NoError(t, json.Unmarshal(raw, &failed))
	require.Equal(t, 3, failed.Failed)
	require.Zero(t, failed.Created)
	require.NotEmpty(t, failed.Errors)

	// Happy path, then bulk delete.
	q = base()
	q.Set("period", "1m")
	status, raw = bulk(http.MethodPost, q)
	require.Equal(t, http.StatusOK, status, string(raw))
	var created BulkResp
	require.NoError(t, json.Unmarshal(raw, &created))
	require.Equal(t, 3, created.Created)
	require.Equal(t, "bulk-0", created.FirstSlug)
	require.Equal(t, "bulk-2", created.LastSlug)

	// Delete validation.
	for _, mutate := range []func(url.Values){
		func(q url.Values) { q.Del("slug") },
		func(q url.Values) { q.Set("slug", "bulk") },
		func(q url.Values) { q.Del("count") },
		func(q url.Values) { q.Set("org", "nope") },
	} {
		dq := base()
		mutate(dq)
		status, _ = bulk(http.MethodDelete, dq)
		require.Equal(t, http.StatusBadRequest, status)
	}

	// 5 requested but only 3 exist: unknown slugs are skipped.
	q = base()
	q.Set("count", "5")
	status, raw = bulk(http.MethodDelete, q)
	require.Equal(t, http.StatusOK, status, string(raw))
	var deleted struct {
		Deleted int `json:"deleted"`
	}
	require.NoError(t, json.Unmarshal(raw, &deleted))
	require.Equal(t, 3, deleted.Deleted)

	// DeleteAllChecks: recreate then wipe.
	status, _ = bulk(http.MethodPost, base())
	require.Equal(t, http.StatusOK, status)
	status, raw = doBearerRequest(t, ts, "", http.MethodDelete, "/api/v1/test/checks/all?org="+TestOrgSlug, nil)
	require.Equal(t, http.StatusOK, status, string(raw))
	require.NoError(t, json.Unmarshal(raw, &deleted))
	require.Equal(t, 3, deleted.Deleted)

	status, _ = doBearerRequest(t, ts, "", http.MethodDelete, "/api/v1/test/checks/all?org=nope", nil)
	require.Equal(t, http.StatusBadRequest, status)
}

// BulkResp mirrors testapi.BulkCreateChecksResponse.
type BulkResp struct {
	Created   int      `json:"created"`
	Failed    int      `json:"failed"`
	Errors    []string `json:"errors"`
	FirstSlug string   `json:"firstSlug"`
	LastSlug  string   `json:"lastSlug"`
}
