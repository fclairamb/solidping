package checks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// This file covers spec 2026-08-28-14: POST /checks/validate must agree with
// POST /checks on every request-level guard, not just config. The property
// under test is AGREEMENT — a table of payloads driven through both HTTP
// endpoints, asserting accept/reject matches — because the bug was never "one
// endpoint has a wrong rule", it was "the two endpoints disagree by
// construction" (different request structs).
//
// The two endpoints do NOT share a response shape: create's `internal` guard
// answers 422 with a `fields[]` array (spec 2026-08-27-01), but the other
// request-level guards here predate this spec and already answer 400 with a
// flat `{title,code,detail}` — no `fields[]` at all. That asymmetry is
// pre-existing and out of this spec's scope (see the spec's Decisions); what
// matters for parity is that both endpoints reject the SAME payloads, which
// is what every message assertion below is anchored to (every sentinel error
// message here leads with the field name, so a substring check on create's
// `detail` and an exact check on validate's `fields[0].name` are testing the
// same rule).

// newParityRouter builds a checks handler with both POST /checks and POST
// /checks/validate wired over a fresh in-memory db, mirroring the real
// server's route registration (internal/app/server.go).
func newParityRouter(t *testing.T) (*httpx.Router, string) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("parity-org", "Parity Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.POST("", handler.CreateCheck)
	group.POST("/validate", handler.ValidateCheck)

	return router, org.Slug
}

// postJSON POSTs a JSON body to path and returns the decoded response.
func postJSON(t *testing.T, router *httpx.Router, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	r := require.New(t)

	raw, err := json.Marshal(body)
	r.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var decoded map[string]any
	if rec.Body.Len() > 0 {
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &decoded))
	}

	return rec.Code, decoded
}

// parityCase is one payload driven through both endpoints.
type parityCase struct {
	name string
	// extra is merged onto the base {"type":"http","config":{"url":...}}
	// payload; both endpoints see the exact same body.
	extra map[string]any
	// wantAccepted is true when both create (201) and validate (valid:true)
	// must accept the payload; false when both must refuse it.
	wantAccepted bool
	// wantCreateStatus is the status create must answer with when refused.
	wantCreateStatus int
	// wantCreateDetailPrefix is the prefix create's error detail/title must
	// carry when wantCreateStatus is 400 (no fields[] on that shape).
	wantCreateDetailPrefix string
	// wantFieldName is validate's fields[0].name, and (only for the 422
	// shape) create's fields[0].name too.
	wantFieldName string
	// wantMessage, when non-empty, is the exact fields[0].message both
	// responses must carry — only set where both use the identical
	// unwrapped message (the internal-field guard).
	wantMessage string
}

// TestValidateCreateRequestFieldParity is the parity property itself: every
// row is driven through BOTH POST /checks and POST /checks/validate on a
// fresh org, and the two must agree on accept/reject. This is what regressed
// (validate answered 200 valid:true for a body create refused with 422) and
// what stops a future field from drifting the same way.
func TestValidateCreateRequestFieldParity(t *testing.T) {
	t.Parallel()

	const internalMsg = "The internal flag is read-only: it marks server-created checks and cannot be set by a client"

	cases := []parityCase{
		// --- internal: the bug report's own field, and the positive control ---
		{
			name:             "internal true is refused",
			extra:            map[string]any{"internal": true},
			wantCreateStatus: http.StatusUnprocessableEntity,
			wantFieldName:    "internal",
			wantMessage:      internalMsg,
		},
		{
			// The case a partial fix lets through: false reads as harmless,
			// but the create guard fires on ANY non-nil value.
			name:             "internal false is refused",
			extra:            map[string]any{"internal": false},
			wantCreateStatus: http.StatusUnprocessableEntity,
			wantFieldName:    "internal",
			wantMessage:      internalMsg,
		},
		{
			// Positive control: the identical payload minus `internal` must
			// be accepted by both. Without this half, a validate endpoint
			// that rejects everything would also pass the table.
			name:         "no internal field is accepted",
			extra:        map[string]any{},
			wantAccepted: true,
		},

		// --- regionSpread: 0 <= spread < period ---
		{
			name: "regionSpread equal to period is refused",
			extra: map[string]any{
				"period": "00:01:00", "regionSpread": "00:01:00",
			},
			wantCreateStatus:       http.StatusBadRequest,
			wantCreateDetailPrefix: "regionSpread",
			wantFieldName:          "regionSpread",
		},
		{
			name: "regionSpread within period is accepted",
			extra: map[string]any{
				"period": "00:01:00", "regionSpread": "00:00:10",
			},
			wantAccepted: true,
		},

		// --- tracerouteOnFailure enum ---
		{
			name:                   "tracerouteOnFailure garbage value is refused",
			extra:                  map[string]any{"tracerouteOnFailure": "sometimes"},
			wantCreateStatus:       http.StatusBadRequest,
			wantCreateDetailPrefix: "tracerouteOnFailure",
			wantFieldName:          "tracerouteOnFailure",
		},
		{
			name:         "tracerouteOnFailure on is accepted",
			extra:        map[string]any{"tracerouteOnFailure": "on"},
			wantAccepted: true,
		},

		// --- flapping knobs ---
		{
			name:                   "flapBackoffFactor below 1 is refused",
			extra:                  map[string]any{"flapBackoffFactor": 0},
			wantCreateStatus:       http.StatusBadRequest,
			wantCreateDetailPrefix: "flapBackoffFactor",
			wantFieldName:          "flapBackoffFactor",
		},
		{
			name:                   "flappingWindowSeconds negative is refused",
			extra:                  map[string]any{"flappingWindowSeconds": -1},
			wantCreateStatus:       http.StatusBadRequest,
			wantCreateDetailPrefix: "flappingWindowSeconds",
			wantFieldName:          "flappingWindowSeconds",
		},
		{
			name:                   "maxRecoveryMultiplier below 1 is refused",
			extra:                  map[string]any{"maxRecoveryMultiplier": 0},
			wantCreateStatus:       http.StatusBadRequest,
			wantCreateDetailPrefix: "maxRecoveryMultiplier",
			wantFieldName:          "maxRecoveryMultiplier",
		},

		// --- incident periods ---
		{
			name:                   "confirmationPeriodSeconds negative is refused",
			extra:                  map[string]any{"confirmationPeriodSeconds": -1},
			wantCreateStatus:       http.StatusBadRequest,
			wantCreateDetailPrefix: "confirmationPeriodSeconds",
			wantFieldName:          "confirmationPeriodSeconds",
		},
		{
			name:                   "recoveryPeriodSeconds over the one-day cap is refused",
			extra:                  map[string]any{"recoveryPeriodSeconds": 86401},
			wantCreateStatus:       http.StatusBadRequest,
			wantCreateDetailPrefix: "recoveryPeriodSeconds",
			wantFieldName:          "recoveryPeriodSeconds",
		},
		{
			name:         "in-range incident periods are accepted",
			extra:        map[string]any{"confirmationPeriodSeconds": 60, "recoveryPeriodSeconds": 60},
			wantAccepted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			// Each case gets its own org/router: create mutates state
			// (inserts rows, consumes slugs) and the cases must not interact.
			router, orgSlug := newParityRouter(t)

			body := map[string]any{
				"type":   "http",
				"config": map[string]any{"url": "https://example.com"},
			}
			for k, v := range tc.extra {
				body[k] = v
			}

			validateStatus, validateBody := postJSON(t, router, "/api/v1/orgs/"+orgSlug+"/checks/validate", body)
			r.Equal(http.StatusOK, validateStatus, "validate must always answer 200: %v", validateBody)

			createStatus, createBody := postJSON(t, router, "/api/v1/orgs/"+orgSlug+"/checks", body)

			valid, _ := validateBody["valid"].(bool)
			fields, _ := validateBody["fields"].([]any)

			if tc.wantAccepted {
				r.True(valid, "validate should accept: %v", validateBody)
				r.Empty(fields, "valid:true must carry no blocking fields: %v", validateBody)
				r.Equal(http.StatusCreated, createStatus, "create should accept: %v", createBody)

				return
			}

			// Refused by both — the core parity assertion.
			r.False(valid, "validate should refuse: %v", validateBody)
			r.NotEmpty(fields, "valid:false must carry the blocking field: %v", validateBody)
			// valid is false exactly when fields is non-empty (spec
			// 2026-08-26-05's invariant) — checked on every negative row,
			// not just internal's.
			r.Equal(len(fields) > 0, !valid)

			firstField, ok := fields[0].(map[string]any)
			r.True(ok, "fields[0] must be an object: %v", validateBody)
			r.Equal(tc.wantFieldName, firstField["name"], "validate fields[0].name: %v", validateBody)
			if tc.wantMessage != "" {
				r.Equal(tc.wantMessage, firstField["message"], "validate fields[0].message: %v", validateBody)
			}

			r.Equal(tc.wantCreateStatus, createStatus, "create status: %v", createBody)
			r.Equal(string(base.ErrorCodeValidationError), createBody["code"], "create code: %v", createBody)

			switch tc.wantCreateStatus {
			case http.StatusUnprocessableEntity:
				createFields, _ := createBody["fields"].([]any)
				r.NotEmpty(createFields, "create's 422 must carry fields[]: %v", createBody)
				createField, fieldOK := createFields[0].(map[string]any)
				r.True(fieldOK, "create fields[0] must be an object: %v", createBody)
				r.Equal(tc.wantFieldName, createField["name"], "create fields[0].name: %v", createBody)
				if tc.wantMessage != "" {
					r.Equal(tc.wantMessage, createField["message"], "create fields[0].message: %v", createBody)
				}
			case http.StatusBadRequest:
				detail, _ := createBody["detail"].(string)
				r.True(strings.HasPrefix(detail, tc.wantCreateDetailPrefix),
					"create detail %q must start with %q: %v", detail, tc.wantCreateDetailPrefix, createBody)
			}
		})
	}
}
