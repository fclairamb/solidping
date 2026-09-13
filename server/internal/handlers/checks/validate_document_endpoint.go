package checks

import (
	"context"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ValidateDocumentResponse is what POST /api/v1/orgs/:org/checks/validate
// answers when the body is a whole export/manifest document rather than a
// single check definition.
//
// Two properties it exists for (spec 2026-09-11-04):
//
//   - EVERY issue, never first-error-only. A CI job that learns of one problem
//     per round trip is a CI job nobody runs.
//   - A stable `code` per issue, so that job can allow-list the classes it
//     accepts (an org that deliberately inlines a `username`, say) without
//     pattern-matching English prose. DocumentIssueCodes() is the closed set.
type ValidateDocumentResponse struct {
	// Valid is true exactly when Issues is empty.
	Valid bool `json:"valid"`
	// Issues is every problem found, in document order; never null.
	Issues []DocumentIssue `json:"issues"`
	// Plan is the reconcile plan (`?plan=true`, admin only): what applying
	// this document would create, update, leave unchanged, delete or refuse to
	// adopt. Absent when not requested, and when the document is malformed
	// enough that no plan can be computed.
	Plan *ApplyResult `json:"plan,omitempty"`
}

// IsDocumentBody reports whether a request body is a whole export/manifest
// document — a mapping with a top-level `checks` list — as opposed to a single
// check definition. JSON first, then YAML, mirroring ParseManifest's sniffing
// and the CLI's isExportDocumentShape.
//
// This is what makes POST /checks/validate content-negotiated rather than a
// second endpoint: the caller posts what it has, and the server answers about
// the thing it was given.
func IsDocumentBody(body []byte) bool {
	var generic map[string]any

	if jsonErr := json.Unmarshal(body, &generic); jsonErr != nil {
		if yamlErr := yaml.Unmarshal(body, &generic); yamlErr != nil {
			return false
		}
	}

	_, ok := generic["checks"].([]any)

	return ok
}

// ValidateDocumentForOrg validates a whole document FOR ONE ORGANIZATION: the
// offline format rules (ValidateDocument) plus the one rule that needs the
// org's own rows — that every ${env:}/${param:} reference resolves.
//
// It writes nothing, which is why the route is member-level: a CI job that only
// wants to know "is this file valid?" must not need a write-capable token. The
// plan (`withPlan`) is the admin-only half, because it reads the org's whole
// check set to answer.
func (s *Service) ValidateDocumentForOrg(
	ctx context.Context, orgSlug string, doc *ExportDocument, withPlan bool,
) (ValidateDocumentResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ValidateDocumentResponse{}, ErrOrganizationNotFound
	}

	// Knowing which slugs already exist is what keeps this endpoint's answer
	// and the write path's answer the same. A `secrets: stripped` document
	// legitimately omits a declared secret for a check that EXISTS (the import
	// merge restores it) and illegitimately for one that does not (a create has
	// nothing to merge, and /import refuses it). Offline, ValidateDocument has
	// to assume the former; here we can tell.
	exists := s.existingSlugPredicate(ctx, org.UID)

	issues := validateDocumentAgainst(doc, exists)

	issues = append(issues, s.secretRefIssues(ctx, org.UID, doc)...)

	if issues == nil {
		issues = []DocumentIssue{}
	}

	resp := ValidateDocumentResponse{Valid: len(issues) == 0, Issues: issues}

	if withPlan {
		// A plan that cannot be computed is simply absent — the issues above
		// already say why, and answering 500 for a document the caller asked us
		// to find fault with would be the wrong shape entirely.
		if plan, planErr := s.ApplyChecks(ctx, orgSlug, doc, ApplyOptions{DryRun: true}); planErr == nil {
			resp.Plan = plan
		}
	}

	return resp, nil
}

// existingSlugPredicate returns "does this slug already exist in the org?",
// backed by one query rather than one per check.
//
// A failed lookup answers FALSE for everything, which is the conservative
// direction: the validator then reports the stripped-secret complaints rather
// than suppressing them, so a transient database problem can produce noise but
// never a false all-clear.
func (s *Service) existingSlugPredicate(ctx context.Context, orgUID string) func(string) bool {
	rows, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{})
	if err != nil {
		return func(string) bool { return false }
	}

	slugs := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Slug != nil && *row.Slug != "" {
			slugs[*row.Slug] = struct{}{}
		}
	}

	return func(slug string) bool {
		_, ok := slugs[slug]

		return ok
	}
}

// secretRefIssues reports one issue per check whose config carries a reference
// that does not resolve for this organization. It is the org-aware half of the
// validation: ValidateDocument performs no I/O by design (it is what `sp checks
// validate` runs with no token and no network), so it cannot know whether
// `${param:sso-password}` exists.
//
// Both halves read the same resolver the write path uses, so a document this
// endpoint calls valid is one /import and /apply accept.
func (s *Service) secretRefIssues(
	ctx context.Context, orgUID string, doc *ExportDocument,
) []DocumentIssue {
	findings, _ := s.secretRefFindings(ctx, orgUID, doc)
	if len(findings) == 0 {
		return nil
	}

	issues := make([]DocumentIssue, 0, len(findings))

	for i := range findings {
		where := findings[i].where
		if where == "" {
			where = fmt.Sprintf("<check %d>", i)
		}

		issues = append(issues, DocumentIssue{
			Where: where, Field: fieldConfig, Code: CodeUnresolvedSecretRef,
			Message: findings[i].err.Error(),
		})
	}

	return issues
}
