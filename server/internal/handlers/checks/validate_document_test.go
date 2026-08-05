package checks

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// baseValidDocument mirrors the shape of the reference checks-as-code
// workflow's known-good config.yaml: a small but structurally complete v2
// document that must validate clean.
func baseValidDocument() *ExportDocument {
	return &ExportDocument{
		Version:      ExportVersionV2,
		Organization: "stonaltech",
		Secrets:      SecretsMarkerStripped,
		Checks: []ExportCheck{
			{
				Name:   "RabbitMQ prod (aws)",
				Slug:   "rabbitmq-aws-prod",
				Type:   "http",
				Config: map[string]any{"url": "https://broker.example.com/"},
				Labels: map[string]string{"app": "rabbitmq", "cluster": "aws-prod"},
			},
			{
				Name:   "api.stonal.io (http)",
				Slug:   "http-api-stonal-io",
				Type:   "http",
				Config: map[string]any{"expectedStatus": 401, "url": "https://api.stonal.io"},
				Labels: map[string]string{"environment": "prod", "stack": "prod"},
			},
			{
				Name: "api.stonal.io/datalake (http)",
				Slug: "http-api-stonal-io-datalake",
				Type: "http",
				Config: map[string]any{
					"url": "https://api.stonal.io/datalake/mgmt/version",
				},
				Labels: map[string]string{"app": "datalake", "environment": "prod", "stack": "prod"},
				DependsOn: []ExportedDependency{
					{ParentSlug: "http-api-stonal-io", Kind: "hard"},
					{ParentSlug: "rabbitmq-aws-prod", Kind: "hard"},
				},
			},
		},
	}
}

// TestValidateDocumentKnownGood verifies the reference-shaped document
// validates clean end to end (Proposal 4 / spec test area: "validate against
// a known-good document").
func TestValidateDocumentKnownGood(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	issues := ValidateDocument(baseValidDocument())
	r.Empty(issues, "%+v", issues)
}

// TestValidateDocumentGenericRuleViolations exercises each generic rule with
// exactly one violation, mirroring the reference workflow's
// test_validate_config.py table (spec test area: "validate against documents
// violating each generic rule").
func TestValidateDocumentGenericRuleViolations(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(doc *ExportDocument)
		expected string
	}{
		{
			name:     "unsupported version",
			mutate:   func(doc *ExportDocument) { doc.Version = 99 },
			expected: "version must be 1 or 2",
		},
		{
			name:     "missing organization",
			mutate:   func(doc *ExportDocument) { doc.Organization = "" },
			expected: "organization is missing",
		},
		{
			name:     "secrets not stripped",
			mutate:   func(doc *ExportDocument) { doc.Secrets = "raw" },
			expected: "secrets must stay",
		},
		{
			name:     "no checks",
			mutate:   func(doc *ExportDocument) { doc.Checks = nil },
			expected: "checks must be a non-empty list",
		},
		{
			name:     "slug not kebab-case",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Slug = "Not_Kebab" },
			expected: "invalid slug format",
		},
		{
			name: "duplicate slug",
			mutate: func(doc *ExportDocument) {
				doc.Checks[1].Slug = doc.Checks[0].Slug
			},
			expected: "duplicate slug",
		},
		{
			name:     "missing name",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Name = "" },
			expected: "missing required field \"name\"",
		},
		{
			name:     "unknown type",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Type = "gopher" },
			expected: "unsupported check type",
		},
		{
			name:     "config missing",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Config = nil },
			expected: "config is missing or null",
		},
		{
			name:     "http check without url",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Config = map[string]any{} },
			expected: "url",
		},
		{
			name:     "url without a scheme",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Config["url"] = "broker.example.com" },
			expected: "must start with http",
		},
		{
			name:     "credential inlined in config",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Config["username"] = "solidping" },
			expected: "looks like a credential",
		},
		{
			name: "expectedStatus and expectedStatusCodes both set",
			mutate: func(doc *ExportDocument) {
				doc.Checks[1].Config["expectedStatusCodes"] = []string{"200"}
			},
			expected: "supersedes",
		},
		{
			name:     "period is not a duration",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Period = "1 minute" },
			expected: "is not a duration",
		},
		{
			name:     "region is not a slug",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Regions = []string{"Paris FR"} },
			expected: "must be a slug",
		},
		{
			name:     "label with empty value",
			mutate:   func(doc *ExportDocument) { doc.Checks[0].Labels["app"] = "" },
			expected: "must have a non-empty string value",
		},
		{
			name: "dependsOn points nowhere",
			mutate: func(doc *ExportDocument) {
				doc.Checks[2].DependsOn[0].ParentSlug = "http-api-ghost"
			},
			expected: "does not match any check",
		},
		{
			name: "check depends on itself",
			mutate: func(doc *ExportDocument) {
				doc.Checks[2].DependsOn[0].ParentSlug = doc.Checks[2].Slug
			},
			expected: "depends on itself",
		},
		{
			name: "duplicate parent",
			mutate: func(doc *ExportDocument) {
				doc.Checks[2].DependsOn = append(doc.Checks[2].DependsOn,
					ExportedDependency{ParentSlug: "http-api-stonal-io", Kind: "hard"})
			},
			expected: "twice",
		},
		{
			name: "unknown dependency kind",
			mutate: func(doc *ExportDocument) {
				doc.Checks[2].DependsOn[0].Kind = "sort-of"
			},
			expected: "expected \"hard\" or \"soft\"",
		},
		{
			name: "dependency cycle",
			mutate: func(doc *ExportDocument) {
				doc.Checks[1].DependsOn = []ExportedDependency{
					{ParentSlug: "http-api-stonal-io-datalake", Kind: "hard"},
				}
			},
			expected: "dependency cycle",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			doc := baseValidDocument()
			tc.mutate(doc)

			issues := ValidateDocument(doc)
			r.NotEmpty(issues, "expected a violation to be reported")

			found := false
			for _, issue := range issues {
				if strings.Contains(strings.ToLower(issue.Message), strings.ToLower(tc.expected)) {
					found = true

					break
				}
			}
			r.True(found, "expected an issue containing %q, got %+v", tc.expected, issues)
		})
	}
}
