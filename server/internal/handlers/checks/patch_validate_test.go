package checks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// patchCheck marshals body and PATCHes it to an existing check.
func patchCheck(
	t *testing.T, router *httpx.Router, orgSlug, checkUID string, body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	r := require.New(t)

	payload, err := json.Marshal(body)
	r.NoError(err)

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPatch, "/api/v1/orgs/"+orgSlug+"/checks/"+checkUID, bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// getCheck fetches a check by UID.
func getCheck(t *testing.T, router *httpx.Router, orgSlug, checkUID string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/orgs/"+orgSlug+"/checks/"+checkUID, http.NoBody)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestPatchHTTPExpectedStatusCodesTypeRejected is the headline regression:
// CreateCheck already rejects expectedStatusCodes carrying JSON numbers
// instead of strings (checkhttp's HTTPConfig.FromMap), but UpdateCheck used
// to accept and persist it — the config would then only ever fail at the
// worker's parse step. json.Marshal of a Go []int produces JSON numbers on
// the wire, so the server's own json.Decode hands the checker []any of
// float64 elements — the real shape of the bug, not a hand-built []any{200}.
func TestPatchHTTPExpectedStatusCodesTypeRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	created := postCheck(t, router, orgSlug, map[string]any{
		"type":   "http",
		"config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusCreated, created.Code, created.Body.String())

	var createdBody map[string]any
	r.NoError(json.Unmarshal(created.Body.Bytes(), &createdBody))
	uid, _ := createdBody["uid"].(string)
	r.NotEmpty(uid)

	rec := patchCheck(t, router, orgSlug, uid, map[string]any{
		"config": map[string]any{
			"url":                 "https://example.com",
			"expectedStatusCodes": []int{200, 403},
		},
	})
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	var errBody map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &errBody))
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])

	// Nothing must have persisted.
	getRec := getCheck(t, router, orgSlug, uid)
	r.Equal(http.StatusOK, getRec.Code, getRec.Body.String())

	var fetched map[string]any
	r.NoError(json.Unmarshal(getRec.Body.Bytes(), &fetched))
	cfg, _ := fetched["config"].(map[string]any)
	r.NotContains(cfg, "expectedStatusCodes", "the malformed PATCH must not have persisted anything")

	// Positive control: the same PATCH with strings succeeds and persists —
	// without this, a change that rejects everything would also pass.
	rec = patchCheck(t, router, orgSlug, uid, map[string]any{
		"config": map[string]any{
			"url":                 "https://example.com",
			"expectedStatusCodes": []string{"200", "403"},
		},
	})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	getRec = getCheck(t, router, orgSlug, uid)
	r.Equal(http.StatusOK, getRec.Code, getRec.Body.String())
	r.NoError(json.Unmarshal(getRec.Body.Bytes(), &fetched))
	cfg, _ = fetched["config"].(map[string]any)
	r.ElementsMatch([]any{"200", "403"}, cfg["expectedStatusCodes"])
}

// TestPatchHTTPInvalidMethodRejected proves the PATCH-path checker.Validate
// call isn't special-cased to expectedStatusCodes: any per-type structural
// rule must now be enforced, e.g. HTTP's method allow-list.
func TestPatchHTTPInvalidMethodRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	created := postCheck(t, router, orgSlug, map[string]any{
		"type":   "http",
		"config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusCreated, created.Code, created.Body.String())

	var createdBody map[string]any
	r.NoError(json.Unmarshal(created.Body.Bytes(), &createdBody))
	uid, _ := createdBody["uid"].(string)

	rec := patchCheck(t, router, orgSlug, uid, map[string]any{
		"config": map[string]any{"url": "https://example.com", "method": "NOTAVERB"},
	})
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	var errBody map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &errBody))
	r.Equal(string(base.ErrorCodeValidationError), errBody["code"])
	fields, _ := errBody["fields"].([]any)
	r.NotEmpty(fields)
	field, _ := fields[0].(map[string]any)
	r.Equal("method", field["name"])
	r.Contains(field["message"], "invalid HTTP method")

	getRec := getCheck(t, router, orgSlug, uid)
	r.Equal(http.StatusOK, getRec.Code, getRec.Body.String())

	var fetched map[string]any
	r.NoError(json.Unmarshal(getRec.Body.Bytes(), &fetched))
	cfg, _ := fetched["config"].(map[string]any)
	r.NotContains(cfg, "method", "the malformed PATCH must not have persisted anything")
}

// TestPatchHTTPCheckerValidateMutationDoesNotLeakIntoStoredName pins that the
// checker.Validate call the PATCH path now makes runs against a throwaway
// spec: HTTPChecker.Validate auto-fills spec.Name from the URL's hostname
// whenever spec.Name is empty, and the deep-copied/throwaway CheckSpec my
// validation step builds always starts with an empty Name — so on every
// PATCH the checker WILL try to autogenerate one. That must never overwrite
// a check's actual stored name.
func TestPatchHTTPCheckerValidateMutationDoesNotLeakIntoStoredName(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	router, orgSlug := newCheckHandlerRouter(t)

	created := postCheck(t, router, orgSlug, map[string]any{
		"type":   "http",
		"name":   "My Custom Name",
		"config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusCreated, created.Code, created.Body.String())

	var createdBody map[string]any
	r.NoError(json.Unmarshal(created.Body.Bytes(), &createdBody))
	uid, _ := createdBody["uid"].(string)
	r.Equal("My Custom Name", createdBody["name"])

	rec := patchCheck(t, router, orgSlug, uid, map[string]any{
		"config": map[string]any{"url": "https://other.example.com"},
	})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var patched map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &patched))
	r.Equal("My Custom Name", patched["name"],
		"checker.Validate's autogenerated name must not leak into the stored check")
}
