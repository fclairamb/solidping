package checks_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/orgparams"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// newValidateRouter wires POST /checks/validate over a fresh in-memory DB. No
// auth middleware: the authorization split between the document and
// single-check forms is proven against the REAL route table, in
// internal/app/checks_validate_document_authz_test.go.
func newValidateRouter(t *testing.T) (*httpx.Router, *checks.Service, db.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("validate-doc", "Validate Doc Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	svc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), disabledCreds(t), entSvc)
	handler := checks.NewHandler(svc, &config.Config{})

	router := httpx.New()
	router.NewGroup("/api/v1/orgs/:org/checks").POST("/validate", handler.ValidateCheck)

	return router, svc, dbSvc, org
}

func postValidateBody(
	t *testing.T, router *httpx.Router, org, query, contentType string, body []byte,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/orgs/"+org+"/checks/validate"+query, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestValidateDocumentReportsEveryIssueWithAStableCode is the endpoint spec
// 2026-09-11-04 adds, in the form a CI job consumes it.
//
// The document below is wrong in six independent ways spread over three
// checks. A validator that answers with the first one is a validator that
// costs a round trip per defect, which is why nobody used the one that
// existed; and a validator that answers only in prose is one a CI job has to
// pattern-match English to interpret. So: every issue, each with a stable code
// drawn from the closed set the server publishes.
func TestValidateDocumentReportsEveryIssueWithAStableCode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router, _, _, org := newValidateRouter(t)

	// YAML on purpose: a hand-authored manifest is as likely to be YAML as
	// JSON, and the same route has to take both.
	doc := []byte(`version: 2
organization: validate-doc
secrets: stripped
checks:
  - name: Good
    slug: good
    type: http
    config:
      url: https://acme.com/good
  - name: Bad Region
    slug: bad-region
    type: http
    period: 1 minute
    regions: ["Paris!"]
    config:
      url: https://acme.com/bad
      expectedStatus: 200
      expectedStatusCodes: [200]
  - name: Unknown
    slug: good
    type: not-a-check-type
    config: {}
    dependsOn:
      - parentSlug: nowhere
        kind: hard
`)

	rec := postValidateBody(t, router, org.Slug, "", "application/yaml", doc)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp checks.ValidateDocumentResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.False(resp.Valid)
	r.Nil(resp.Plan, "no plan was asked for")

	byCode := map[string][]checks.DocumentIssue{}
	for _, issue := range resp.Issues {
		byCode[issue.Code] = append(byCode[issue.Code], issue)
	}

	// Every code the server can emit is in the published set — that set IS the
	// contract a CI job allow-lists against.
	known := map[string]struct{}{}
	for _, code := range checks.DocumentIssueCodes() {
		known[code] = struct{}{}
	}
	for _, issue := range resp.Issues {
		r.Containsf(known, issue.Code, "issue %+v carries a code outside DocumentIssueCodes()", issue)
		r.NotEmptyf(issue.Where, "issue %+v names no check", issue)
		r.NotEmptyf(issue.Message, "issue %+v carries no message", issue)
	}

	// All six, not the first one.
	r.Contains(byCode, checks.CodeInvalidPeriod)
	r.Contains(byCode, checks.CodeRegionFormat)
	r.Contains(byCode, checks.CodeStatusFieldConflict)
	r.Contains(byCode, checks.CodeDuplicateSlug)
	r.Contains(byCode, checks.CodeUnknownType)
	r.Contains(byCode, checks.CodeInvalidDependsOn)

	// And they are attributed to the right checks, with the offending field
	// named — a plain list of sentences would not let a CI job annotate a diff.
	r.Equal("bad-region", byCode[checks.CodeRegionFormat][0].Where)
	r.Equal("regions", byCode[checks.CodeRegionFormat][0].Field)
	r.Equal("type", byCode[checks.CodeUnknownType][0].Field)

	// Anti-vacuity: the issues really are spread across the document rather
	// than all coming from the one check that fails hardest.
	where := map[string]bool{}
	for _, issue := range resp.Issues {
		where[issue.Where] = true
	}
	r.GreaterOrEqual(len(where), 2, "every issue was attributed to one check: %+v", resp.Issues)
}

// TestValidateAcceptsAValidDocumentAndPlansIt covers the happy path plus
// `?plan=true`: a document that matches the instance answers `valid` with an
// empty issue list and a plan whose counts say so — 0 created, 0 updated, N
// unchanged. That triple is the machine-readable "the file matches", and it is
// the whole reason the endpoint carries a plan at all.
func TestValidateAcceptsAValidDocumentAndPlansIt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router, svc, _, org := newValidateRouter(t)

	// Applied rather than created, so the check carries the managed label and
	// the plan can speak about ownership as well as content: an unowned slug
	// answers `unmanaged`, which is a different statement from `unchanged`.
	_, err := svc.ApplyChecks(t.Context(), org.Slug, &checks.ExportDocument{
		Version: 2, Organization: org.Slug, Checks: []checks.ExportCheck{{
			Name: "Live", Slug: "live", Type: "http", Enabled: true,
			Config: map[string]any{"url": "https://acme.com/live"},
		}},
	}, checks.ApplyOptions{})
	r.NoError(err)

	doc, err := svc.ExportChecks(t.Context(), org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	rendered, err := checks.MarshalExportDocument(doc)
	r.NoError(err)

	rec := postValidateBody(t, router, org.Slug, "?plan=true", "application/json", rendered)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp checks.ValidateDocumentResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.True(resp.Valid, "the server's own export must validate: %v", resp.Issues)
	r.Empty(resp.Issues)
	r.NotNil(resp.Plan)
	r.Equal(0, resp.Plan.Created)
	r.Equal(0, resp.Plan.Updated)
	r.Equal(0, resp.Plan.Deleted)
	r.Equal(0, resp.Plan.Unmanaged)
	r.Equal(1, resp.Plan.Unchanged)
	r.True(resp.Plan.DryRun, "a plan must never be a write")

	// Negative control: the plan is not a rubber stamp. Move a field and the
	// same document plans as an update naming it.
	doc.Checks[0].Config["url"] = "https://acme.com/moved"
	moved, err := checks.MarshalExportDocument(doc)
	r.NoError(err)

	rec = postValidateBody(t, router, org.Slug, "?plan=true", "application/json", moved)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.NotNil(resp.Plan)
	r.Equal(0, resp.Plan.Unchanged)
	r.Equal("config.url", resp.Plan.Plan[0].Changes[0].Field)
}

// TestValidateRoutesASingleCheckBodyToTheOldShape is the content-negotiation
// boundary: the route is shared, so a body that is NOT a document must keep
// answering exactly as it did — `fields`/`warnings`, not `issues`.
func TestValidateRoutesASingleCheckBodyToTheOldShape(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router, _, _, org := newValidateRouter(t)

	rec := postValidateBody(t, router, org.Slug, "", "application/json",
		[]byte(`{"type":"http","slug":"single","config":{"url":"https://acme.com/single"}}`))
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
	r.Equal(true, body["valid"])
	r.NotContains(body, "issues", "a single-check body must not answer in the document shape")

	// A body that is neither is still rejected, not read as an empty document.
	rec = postValidateBody(t, router, org.Slug, "", "application/json", []byte("not json at all"))
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

// TestValidateDocumentReportsUnresolvedSecretRefsPerCheck covers the one rule
// that needs the organization's own rows, and therefore cannot live in the
// offline ValidateDocument that `sp checks validate` runs.
//
// It is also the code spec 2026-09-11-03 made a hard 400 on /import and
// /apply: a document this endpoint calls valid has to be one those two accept,
// or the CI gate is worse than useless.
func TestValidateDocumentReportsUnresolvedSecretRefsPerCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router, svc, _, org := newValidateRouter(t)

	doc := []byte(`version: 2
organization: validate-doc
secrets: stripped
checks:
  - name: One
    slug: one
    type: http
    config:
      url: https://acme.com/one
      body: "token=${param:missing-one}"
  - name: Two
    slug: two
    type: http
    config:
      url: https://acme.com/two
      body: "token=${param:missing-two}"
`)

	rec := postValidateBody(t, router, org.Slug, "", "application/yaml", doc)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp checks.ValidateDocumentResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.False(resp.Valid)

	unresolved := map[string]bool{}
	for _, issue := range resp.Issues {
		if issue.Code == checks.CodeUnresolvedSecretRef {
			unresolved[issue.Where] = true
		}
	}
	r.Truef(unresolved["one"] && unresolved["two"],
		"both unresolved references must be reported, got %+v", resp.Issues)

	// The same document really is refused by the write path, which is the
	// agreement the endpoint exists to let a CI job rely on.
	_, importErr := svc.ImportChecks(t.Context(), org.Slug, mustParse(t, doc), true)
	r.ErrorIs(importErr, checks.ErrUnresolvedSecretRef)
}

// TestValidateDocumentAcceptsAReferenceThatResolves is the positive control
// for the test above: with the parameter present, the very same document is
// valid — so the code above reports a missing parameter and not merely "this
// document uses references".
func TestValidateDocumentAcceptsAReferenceThatResolves(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router, _, dbSvc, org := newValidateRouter(t)

	secret := true
	_, err := orgparams.NewService(dbSvc).Set(t.Context(), org.Slug, "present",
		&orgparams.SetRequest{Value: "secret-value", Secret: &secret})
	r.NoError(err)

	doc := []byte(`version: 2
organization: validate-doc
secrets: stripped
checks:
  - name: One
    slug: one
    type: http
    config:
      url: https://acme.com/one
      body: "token=${param:present}"
`)

	rec := postValidateBody(t, router, org.Slug, "", "application/yaml", doc)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp checks.ValidateDocumentResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.True(resp.Valid, "a reference that resolves is not an issue: %v", resp.Issues)
}

func mustParse(t *testing.T, body []byte) *checks.ExportDocument {
	t.Helper()

	doc, err := checks.ParseManifest(body, "application/yaml")
	require.NoError(t, err)

	return doc
}
