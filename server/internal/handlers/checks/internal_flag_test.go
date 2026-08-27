package checks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// This file covers spec 2026-08-27-01: `internal` is a server-owned flag, not a
// writable request field. It is the flag that exempts a check from the
// MaxChecks quota, from the checks-per-minute demand figure and (since the same
// spec) from the per-org rate gates — so a client able to set it would own
// checks that count nowhere.
//
// Every negative here is paired with a positive control, because "the write was
// refused" is only meaningful next to proof that the legitimate path still
// works.

// boolPtr is a local pointer helper: these tests need to send an EXPLICIT
// `internal`, which is exactly what a nil pointer cannot express.
func boolPtr(v bool) *bool { return &v }

// TestCreateCheckRejectsClientInternal proves the create path refuses the
// field. Both values are refused, not just `true`: the contract is "this field
// is not yours to write", and accepting `internal: false` would leave callers
// believing the property is theirs to control.
func TestCreateCheckRejectsClientInternal(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := setupQuotaService(t, 100)

	for _, value := range []bool{true, false} {
		req := httpCheckReq()
		req.Internal = boolPtr(value)

		_, err := svc.CreateCheck(ctx, org.Slug, req)
		r.ErrorIs(err, checks.ErrInternalFieldNotWritable, "internal:%v must be refused", value)
	}

	// Positive control: the same request without the field is created, and the
	// stored check is not internal.
	created, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)
	r.NotNil(created.Internal)
	r.False(*created.Internal)
}

// TestCreateCheckInternalNoLongerBypassesCap is the loophole itself, closed:
// before this spec, `internal: true` skipped the MaxChecks guard entirely, so a
// capped org could mint unlimited checks by setting one boolean.
func TestCreateCheckInternalNoLongerBypassesCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := setupQuotaService(t, 1)

	// One non-internal check uses up the cap of 1.
	_, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	// The old bypass: `internal: true` is now refused outright, so it can no
	// longer be used to get past the cap.
	req := httpCheckReq()
	req.Internal = boolPtr(true)
	_, err = svc.CreateCheck(ctx, org.Slug, req)
	r.ErrorIs(err, checks.ErrInternalFieldNotWritable)
	r.NotErrorIs(err, entcore.ErrEntitlementExceeded,
		"the field is refused before the quota is even consulted")

	// And a plain second create still hits the cap — proving the guard that the
	// bypass used to skip is intact.
	_, err = svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.ErrorIs(err, entcore.ErrEntitlementExceeded)
}

// TestUpdateCheckRejectsClientInternal covers PATCH: flipping the flag ON would
// un-meter an existing check, and flipping it OFF would re-meter plumbing the
// server owns. Neither is the client's call.
func TestUpdateCheckRejectsClientInternal(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := setupQuotaService(t, 100)

	created, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{
		Internal: boolPtr(true),
	})
	r.ErrorIs(err, checks.ErrInternalFieldNotWritable)

	// The refusal is total: nothing was written, so the check is still metered.
	after, err := svc.GetCheck(ctx, org.Slug, created.UID, checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(after.Internal)
	r.False(*after.Internal, "a refused PATCH must not have flipped the flag")

	// Positive control: an ordinary PATCH on the same check still applies.
	name := "Renamed"
	updated, err := svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Name: &name})
	r.NoError(err)
	r.NotNil(updated.Name)
	r.Equal(name, *updated.Name)
}

// TestUpsertCheckRejectsClientInternal covers PUT-by-slug, the third door into
// the same write. It is refused whether the upsert would create or update, so a
// caller cannot get the flag in by pre-creating the check.
func TestUpsertCheckRejectsClientInternal(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := setupQuotaService(t, 100)

	upsertReq := func(internal *bool) *checks.UpsertCheckRequest {
		return &checks.UpsertCheckRequest{
			Name:     "Upserted",
			Type:     "http",
			Config:   map[string]any{"url": "https://example.com"},
			Internal: internal,
		}
	}

	// Create branch.
	_, _, err := svc.UpsertCheck(ctx, org.Slug, "upsert-target", upsertReq(boolPtr(true)))
	r.ErrorIs(err, checks.ErrInternalFieldNotWritable)

	// Positive control: same body without the field creates the check.
	_, created, err := svc.UpsertCheck(ctx, org.Slug, "upsert-target", upsertReq(nil))
	r.NoError(err)
	r.True(created)

	// Update branch, now that the check exists.
	_, _, err = svc.UpsertCheck(ctx, org.Slug, "upsert-target", upsertReq(boolPtr(true)))
	r.ErrorIs(err, checks.ErrInternalFieldNotWritable)

	stored, err := svc.GetCheck(ctx, org.Slug, "upsert-target", checks.GetCheckOptions{})
	r.NoError(err)
	r.NotNil(stored.Internal)
	r.False(*stored.Internal)
}

// TestImportRejectsInternalDocument covers config-as-code: an import/apply
// document is client-supplied input like any request body. This server never
// PRODUCES a document carrying `internal` (export lists with the default
// `internal = FALSE` filter), so such a document is hand-written.
func TestImportRejectsInternalDocument(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := setupApplyService(t, false)

	body := []byte(`{
      "version": 2,
      "checks": [
        {"name": "sneaky", "slug": "sneaky", "type": "http",
         "config": {"url": "https://sneaky.example.com"}, "internal": true},
        {"name": "honest", "slug": "honest", "type": "http",
         "config": {"url": "https://honest.example.com"}}
      ]
    }`)

	var doc checks.ExportDocument
	r.NoError(json.Unmarshal(body, &doc))

	result, err := svc.ImportChecks(ctx, org.Slug, &doc, false)
	r.NoError(err)
	r.Len(result.Errors, 1, "exactly the internal-bearing check must fail")
	r.Equal("sneaky", result.Errors[0].Slug)
	r.Contains(result.Errors[0].Error, "internal")

	// The refused check was not written at all...
	sneaky, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "sneaky")
	r.True(err != nil || sneaky == nil, "the refused check must not exist")

	// ...while its honest sibling in the same document was imported normally:
	// one bad entry does not sink the whole apply.
	honest, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, "honest")
	r.NoError(err)
	r.NotNil(honest)
	r.False(honest.Internal)
	r.Equal(1, result.Created)
}

// TestValidateDocumentFlagsInternal keeps `validate` and `apply` in agreement:
// a manifest the applier will refuse must not pass validation first. An
// operator who validates in CI and applies in the pipeline would otherwise
// discover the refusal only in the deploy.
func TestValidateDocumentFlagsInternal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	good := checks.ExportCheck{
		Name: "sneaky", Slug: "sneaky", Type: "http",
		Config: map[string]any{"url": "https://sneaky.example.com"},
	}

	// Positive control first: the identical check without the flag is clean.
	doc := &checks.ExportDocument{Version: 2, Organization: "acme", Checks: []checks.ExportCheck{good}}
	r.Empty(checks.ValidateDocument(doc))

	bad := good
	bad.Internal = true
	doc = &checks.ExportDocument{Version: 2, Organization: "acme", Checks: []checks.ExportCheck{bad}}

	issues := checks.ValidateDocument(doc)
	r.Len(issues, 1)
	r.Equal("sneaky", issues[0].Where)
	r.Contains(issues[0].Message, "internal")
}

// TestCloneNeverProducesAnInternalCheck closes the same loophole one door
// further in. An org can see its worker plumbing checks by slug, and the clone
// path used to copy `source.Internal` AND skip the quota for internal clones —
// so cloning one minted an unmetered check without ever sending the field.
func TestCloneNeverProducesAnInternalCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := setupApplyService(t, false)

	// The server-side creation path (what checkworker/jobworker do): straight
	// through the db service, bypassing the checks service entirely.
	source := models.NewCheck(org.UID, "int-checks-worker-1", "http")
	source.Config = models.JSONMap{"url": "https://internal.example.com"}
	source.Internal = true
	source.Enabled = false
	r.NoError(dbSvc.CreateCheck(ctx, source))

	clone, err := svc.CloneCheck(ctx, org.Slug, source.UID, &checks.CloneCheckRequest{})
	r.NoError(err)
	r.NotNil(clone.Internal)
	r.False(*clone.Internal, "a client-triggered clone is never internal")

	r.NotNil(clone.Slug)
	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, *clone.Slug)
	r.NoError(err)
	r.False(stored.Internal)

	// Positive control on the other side of the same change: the SOURCE is
	// still internal — cloning it must not have rewritten the server's own row.
	sourceAfter, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, source.UID)
	r.NoError(err)
	r.True(sourceAfter.Internal)
}

// TestCloneOfInternalCheckIsMetered is the quota half of the clone fix: the
// clone counts against MaxChecks like any other customer check, where before it
// inherited the source's exemption.
func TestCloneOfInternalCheckIsMetered(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := setupQuotaService(t, 1)

	internalSource := models.NewCheck(org.UID, "int-checks-worker-2", "http")
	internalSource.Config = models.JSONMap{"url": "https://internal.example.com"}
	internalSource.Internal = true
	internalSource.Enabled = false
	r.NoError(dbSvc.CreateCheck(ctx, internalSource))

	// The org's one allowed check.
	_, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	// Cloning the internal check would be check #2 → refused by the cap.
	_, err = svc.CloneCheck(ctx, org.Slug, internalSource.UID, &checks.CloneCheckRequest{})
	r.ErrorIs(err, entcore.ErrEntitlementExceeded)
}

// TestServerSideInternalChecksStillCountNowhere is the positive control for the
// whole spec: the two legitimate creators (checkworker/jobworker) write through
// the db service, and their checks must keep their exemptions — from MaxChecks
// and from the demand figure alike. If this test goes red the fix has broken
// the plumbing it was supposed to leave alone.
func TestServerSideInternalChecksStillCountNowhere(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("plumbing-org", "Plumbing Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	r.NoError(entSvc.Set(ctx, org.UID, entcore.Entitlements{
		Limits: entcore.Limits{MaxChecks: entcore.Int(1)},
		Source: models.EntitlementSourceAdmin,
	}, "user:test", ""))

	// The worker's self-stat check, created exactly the way
	// checkworker.createInternalCheck creates it.
	internalCheck := models.NewCheck(org.UID, "int-checks-worker-3", "checkworker")
	internalCheck.Internal = true
	internalCheck.Enabled = false
	r.NoError(dbSvc.CreateCheck(ctx, internalCheck))

	stored, err := dbSvc.GetCheckByUidOrSlug(ctx, org.UID, internalCheck.UID)
	r.NoError(err)
	r.True(stored.Internal, "the server-side path still produces internal checks")

	// Exempt from MaxChecks: the org's single allowed slot is still free.
	r.NoError(entSvc.CheckCreateAllowed(ctx, org.UID))

	// Invisible to the demand figure: ListOrgCheckRates filters internal out,
	// so an internal check contributes no checks-per-minute demand.
	rates, err := dbSvc.ListOrgCheckRates(ctx, org.UID)
	r.NoError(err)
	r.Empty(rates, "an internal check must not appear in the rate list")
}

// TestCreateCheckHandlerRejectsInternalField is the wire-level contract: a
// VALIDATION_ERROR that NAMES the field. A caller has to be able to tell which
// property the server will not take, or it just retries the same body.
func TestCreateCheckHandlerRejectsInternalField(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("internal-h", "Internal Handler Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	router.NewGroup("/api/v1/orgs/:org/checks").POST("", handler.CreateCheck)

	post := func(payload map[string]any) *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(payload)
		r.NoError(marshalErr)
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodPost, "/api/v1/orgs/"+org.Slug+"/checks", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		return rec
	}

	rec := post(map[string]any{
		"type":     "http",
		"config":   map[string]any{"url": "https://example.com"},
		"internal": true,
	})
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	var body struct {
		Code   string                      `json:"code"`
		Fields []base.ValidationErrorField `json:"fields"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(string(base.ErrorCodeValidationError), body.Code)
	r.Len(body.Fields, 1)
	r.Equal("internal", body.Fields[0].Name)
	r.NotEmpty(body.Fields[0].Message)

	// Positive control: the same payload minus the field is created, so the
	// refusal is about `internal` and nothing else.
	rec = post(map[string]any{
		"type":   "http",
		"config": map[string]any{"url": "https://example.com"},
	})
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	// And no internal check was written by the refused request.
	all, _, err := dbSvc.ListChecks(ctx, org.UID, &models.ListChecksFilter{Internal: strPtr("all")})
	r.NoError(err)
	r.Len(all, 1)
	r.False(all[0].Internal)
}
