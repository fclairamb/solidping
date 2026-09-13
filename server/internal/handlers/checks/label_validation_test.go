package checks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// badLabelKeys are the keys the old import regex accepted and the database
// has always refused: two chars, a leading digit, and a dot.
func badLabelKeys() []string { return []string{"os", "1abc", "k8s.cluster"} }

// labelRig is a checks service + router + the db underneath it, so a test can
// drive the HTTP surface and then assert on what did (or did not) reach disk.
type labelRig struct {
	router *httpx.Router
	dbSvc  db.Service
	org    *models.Organization
}

// newLabelRig wires POST/PATCH/GET /checks and POST /checks/import over one
// fresh in-memory database. dbWrap, when non-nil, gets a chance to decorate the
// db.Service the checks service talks to — that is how the atomicity test
// injects a failure after the check insert.
func newLabelRig(t *testing.T, slug string, dbWrap func(db.Service) db.Service) *labelRig {
	t.Helper()

	return newLabelRigWithCreds(t, slug, dbWrap, disabledCreds(t))
}

// newLabelRigWithCreds is newLabelRig with the credentials service spelled
// out, so a test can run against a real KEK and get checks whose config is
// genuinely encrypted at rest.
func newLabelRigWithCreds(
	t *testing.T, slug string, dbWrap func(db.Service) db.Service, creds credentials.Service,
) *labelRig {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "Acme "+slug)
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	var wired db.Service = dbSvc
	if dbWrap != nil {
		wired = dbWrap(dbSvc)
	}

	entSvc := entcore.NewService(wired, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(wired, notifier.NewLocalEventNotifier(), creds, entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.POST("", handler.CreateCheck)
	group.GET("/:checkUid", handler.GetCheck)
	group.PATCH("/:checkUid", handler.UpdateCheck)
	group.POST("/import", handler.ImportChecks)

	return &labelRig{router: router, dbSvc: dbSvc, org: org}
}

// do issues one JSON request against the rig's router.
func (rig *labelRig) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequestWithContext(t.Context(), method,
		"/api/v1/orgs/"+rig.org.Slug+"/checks"+path, reader)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, req)

	return rec
}

// importDoc issues an import (optionally a dry run) and decodes the result.
func (rig *labelRig) importDoc(t *testing.T, doc map[string]any, dryRun bool) checks.ImportResult {
	t.Helper()

	path := "/import"
	if dryRun {
		path += "?dryRun=true"
	}

	rec := rig.do(t, http.MethodPost, path, doc)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var result checks.ImportResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))

	return result
}

// countChecks returns how many checks the org currently holds.
func (rig *labelRig) countChecks(t *testing.T) int {
	t.Helper()

	list, _, err := rig.dbSvc.ListChecks(t.Context(), rig.org.UID, &models.ListChecksFilter{})
	require.NoError(t, err)

	return len(list)
}

// httpCheck is one valid check entry, with whatever labels the caller wants.
func httpCheck(slug string, labels map[string]string) map[string]any {
	entry := map[string]any{
		"name": "Acme " + slug, "slug": slug, "type": "http", "enabled": true,
		"config": map[string]any{"url": "https://example.com/" + slug},
	}
	if labels != nil {
		entry["labels"] = labels
	}

	return entry
}

// importDocument wraps check entries in a v1 export document.
func importDocument(org string, entries ...map[string]any) map[string]any {
	return map[string]any{"version": 1, "organization": org, "checks": entries}
}

// TestImportRejectsLabelKeysTheDatabaseRefuses is regression test 1 of spec
// 2026-09-10-01: a key the DB cannot store must fail validation UP FRONT, with
// a message naming the key and the rule — never a raw SQLSTATE, never a
// doubled "failed to create label: failed to create label:" prefix — and
// nothing may be written.
func TestImportRejectsLabelKeysTheDatabaseRefuses(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newLabelRig(t, "lbl-import", nil)

	entries := make([]map[string]any, 0, len(badLabelKeys())+1)
	for i, key := range badLabelKeys() {
		entries = append(entries, httpCheck(
			[]string{"bad-key-one", "bad-key-two", "bad-key-three"}[i],
			map[string]string{key: "value"}))
	}

	entries = append(entries, httpCheck("bad-value", map[string]string{"environment": ""}))

	result := rig.importDoc(t, importDocument(rig.org.Slug, entries...), false)

	r.Equal(0, result.Created)
	r.Equal(0, result.Updated)
	r.Len(result.Errors, len(entries))

	for i, importErr := range result.Errors {
		msg := importErr.Error

		if i < len(badLabelKeys()) {
			r.Contains(msg, "label key is invalid")
			r.Contains(msg, `"`+badLabelKeys()[i]+`"`, "the message must name the offending key")
			r.Contains(msg, models.LabelKeyPatternSource, "the message must name the rule")
		} else {
			r.Contains(msg, "label value is invalid")
		}

		// Negative controls: no schema detail, no doubled prefix, and no
		// half-written state reported.
		r.NotContains(msg, "SQLSTATE")
		r.NotContains(msg, "labels_key_check")
		r.NotContains(strings.ToLower(msg), "failed to create label: failed to create label")
		r.Empty(importErr.State, "nothing was written, so no item is created-incomplete")
	}

	// Nothing reached the database — not the checks, not the labels.
	r.Equal(0, rig.countChecks(t))

	labelKeys, err := rig.dbSvc.ListDistinctLabelKeys(t.Context(), rig.org.UID, "", 50)
	r.NoError(err)
	r.Empty(labelKeys)
}

// TestCreateAndUpdateRejectLabelKeysTheDatabaseRefuses is the API half of
// regression test 1: the same keys must be a 400 VALIDATION_ERROR on
// POST /checks and PATCH /checks/$uid, not a 500 carrying a driver string.
func TestCreateAndUpdateRejectLabelKeysTheDatabaseRefuses(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newLabelRig(t, "lbl-api", nil)

	// Positive control first: a conformant label is accepted, and the check it
	// is attached to really is created.
	rec := rig.do(t, http.MethodPost, "", map[string]any{
		"name": "Good", "slug": "good-check", "type": "http",
		"config": map[string]any{"url": "https://example.com/good"},
		"labels": map[string]string{"environment": "prod"},
	})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	uid, _ := created["uid"].(string)
	r.NotEmpty(uid)

	for _, key := range append(badLabelKeys(), "") {
		labels := map[string]string{"environment": "prod"}
		if key == "" {
			// The value rule, through the same helper.
			labels["environment"] = ""
		} else {
			labels[key] = "x"
		}

		rec = rig.do(t, http.MethodPost, "", map[string]any{
			"name": "Bad", "slug": "bad-" + key + "check", "type": "http",
			"config": map[string]any{"url": "https://example.com/bad"},
			"labels": labels,
		})
		r.Equal(http.StatusBadRequest, rec.Code, "POST with label %q: %s", key, rec.Body.String())

		var body map[string]any
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
		r.Equal(string(base.ErrorCodeValidationError), body["code"])
		r.NotContains(rec.Body.String(), "SQLSTATE")

		rec = rig.do(t, http.MethodPatch, "/"+uid, map[string]any{"labels": labels})
		r.Equal(http.StatusBadRequest, rec.Code, "PATCH with label %q: %s", key, rec.Body.String())

		r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
		r.Equal(string(base.ErrorCodeValidationError), body["code"])
	}

	// The failed PATCHes changed nothing: the check still carries exactly the
	// label it was created with.
	labels, err := rig.dbSvc.GetLabelsForCheck(t.Context(), uid)
	r.NoError(err)
	r.Len(labels, 1)
	r.Equal("environment", labels[0].Key)
	r.Equal("prod", labels[0].Value)

	// Only the one good check exists.
	r.Equal(1, rig.countChecks(t))
}

// TestSQLiteLabelKeyCheckMatchesPostgres proves migration 021 put the rule in
// the SQLite schema too, not only in Go — the Go gate is what produces good
// messages, the CHECK is what makes the two backends store the same thing.
func TestSQLiteLabelKeyCheckMatchesPostgres(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("lbl-sql", "Acme SQL")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// The legacy managed-label spelling is in the table too: it is exactly the
	// kind of key Postgres has always refused.
	for _, key := range append(badLabelKeys(), checks.LegacyManagedLabelKey) {
		_, err = dbSvc.GetOrCreateLabel(ctx, org.UID, key, "value")
		r.Error(err, "SQLite must refuse label key %q at the SQL level", key)
		r.Contains(err.Error(), "labels_key_check")
	}

	// Positive control: the conformant keys still insert, including the new
	// managed-label spelling /apply stamps.
	for _, key := range []string{"environment", "team", checks.ManagedLabelKey} {
		label, labelErr := dbSvc.GetOrCreateLabel(ctx, org.UID, key, "value")
		r.NoError(labelErr, "key %q must be accepted", key)
		r.NotNil(label)
	}
}

// errPGLabelDrift is a Postgres CHECK violation, worded exactly the way the
// driver words it — SQLSTATE and all. It stands for the one case the
// classifier cannot resolve: the database refusing a key the Go rule accepts.
var errPGLabelDrift = errors.New(
	`ERROR: new row for relation "labels" violates check constraint "labels_key_check" (SQLSTATE=23514)`)

// driftLabelDB makes the database refuse a label key that models.ValidateLabels
// accepts — a Go-rule/CHECK drift, which is a bug in this repo rather than in
// the request.
type driftLabelDB struct {
	db.Service

	driftKey string
}

func (d *driftLabelDB) GetOrCreateLabel(
	ctx context.Context, orgUID, key, value string,
) (*models.Label, error) {
	if key == d.driftKey {
		return nil, errPGLabelDrift
	}

	return d.Service.GetOrCreateLabel(ctx, orgUID, key, value)
}

// TestLabelRuleDriftNeverLeaksTheDriverError covers the defense-in-depth
// branch: when the database refuses a key the Go rule accepted, the caller
// still gets a clean validation message. The driver string — SQLSTATE,
// constraint name, relation name — belongs in the operator's log, not in an
// import report.
func TestLabelRuleDriftNeverLeaksTheDriverError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newLabelRig(t, "lbl-drift", func(inner db.Service) db.Service {
		return &driftLabelDB{Service: inner, driftKey: "environment"}
	})

	result := rig.importDoc(t,
		importDocument(rig.org.Slug, httpCheck("drift-one", map[string]string{"environment": "prod"})), false)

	r.Equal(0, result.Created)
	r.Len(result.Errors, 1)

	msg := result.Errors[0].Error
	r.Contains(msg, "label key is invalid")
	r.Contains(msg, `"environment"`)
	r.Contains(msg, "rejected by the database")

	// The whole point: none of the driver's wording survives into the response.
	r.NotContains(msg, "SQLSTATE")
	r.NotContains(msg, "labels_key_check")
	r.NotContains(msg, "relation")
	r.NotContains(msg, "23514")

	// And it is still a rollback, not a half-written check.
	r.Equal(0, rig.countChecks(t))
}
