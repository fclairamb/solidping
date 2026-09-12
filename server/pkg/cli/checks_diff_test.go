package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// errBoom is a static test-only error, used where a test just needs "any
// error" rather than a specific one.
var errBoom = errors.New("boom")

// TestNormalizeForDiffStripsExportedAt verifies exportedAt never registers as
// drift: two documents differing only in exportedAt normalize identically.
func TestNormalizeForDiffStripsExportedAt(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	docA := []byte(`{"version":2,"exportedAt":"2026-08-01T00:00:00Z","organization":"acme","checks":[]}`)
	docB := []byte(`{"version":2,"exportedAt":"2026-08-05T12:34:56Z","organization":"acme","checks":[]}`)

	linesA, err := normalizeForDiff(docA)
	r.NoError(err)
	linesB, err := normalizeForDiff(docB)
	r.NoError(err)

	r.True(equalLines(linesA, linesB), "documents differing only in exportedAt must normalize identically")
}

// TestNormalizeForDiffDetectsRealDrift verifies a genuine content difference
// still registers.
func TestNormalizeForDiffDetectsRealDrift(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	docA := []byte(`{"version":2,"organization":"acme","checks":[{"slug":"a"}]}`)
	docB := []byte(`{"version":2,"organization":"acme","checks":[{"slug":"b"}]}`)

	linesA, err := normalizeForDiff(docA)
	r.NoError(err)
	linesB, err := normalizeForDiff(docB)
	r.NoError(err)

	r.False(equalLines(linesA, linesB))
}

// TestNormalizeForDiffJSONAndYAMLEquivalent verifies a YAML file and the
// equivalent JSON normalize to the same lines, so diffing a hand-authored
// YAML manifest against the (JSON) live export works.
func TestNormalizeForDiffJSONAndYAMLEquivalent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jsonDoc := []byte(`{"version":2,"organization":"acme","checks":[{"slug":"google","type":"http"}]}`)
	yamlDoc := []byte("version: 2\norganization: acme\nchecks:\n  - slug: google\n    type: http\n")

	jsonLines, err := normalizeForDiff(jsonDoc)
	r.NoError(err)
	yamlLines, err := normalizeForDiff(yamlDoc)
	r.NoError(err)

	r.True(equalLines(jsonLines, yamlLines))
}

// TestComputeDiffOutcomeNoDrift verifies the exact function checksDiffAction
// calls to decide its exit code: identical (modulo exportedAt) documents
// report no drift and no delta text. Spec test area: "diff exit codes ...
// without drift".
func TestComputeDiffOutcomeNoDrift(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	local := []byte(`{"version":2,"exportedAt":"2026-08-01T00:00:00Z","organization":"acme","checks":[{"slug":"a"}]}`)
	live := []byte(`{"version":2,"exportedAt":"2026-08-05T12:34:56Z","organization":"acme","checks":[{"slug":"a"}]}`)

	outcome, err := computeDiffOutcome(local, live, "config.yaml")
	r.NoError(err)
	r.False(outcome.Drift)
	r.Empty(outcome.Delta)
	r.Equal(0, diffExitCode(outcome, nil), "no drift, no error: exit 0")
}

// TestComputeDiffOutcomeWithDrift verifies a genuine content difference
// reports drift and a unified diff naming both sides. Spec test area: "diff
// exit codes with ... drift".
func TestComputeDiffOutcomeWithDrift(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	local := []byte(`{"version":2,"organization":"acme","checks":[{"slug":"a"}]}`)
	live := []byte(`{"version":2,"organization":"acme","checks":[{"slug":"b"}]}`)

	outcome, err := computeDiffOutcome(local, live, "config.yaml")
	r.NoError(err)
	r.True(outcome.Drift)
	r.NotEmpty(outcome.Delta)
	r.Contains(outcome.Delta, "config.yaml")
	r.Contains(outcome.Delta, "solidping (live)")
	r.Equal(1, diffExitCode(outcome, nil), "drift, no error: exit 1")
}

// TestComputeDiffOutcomeError verifies an unparseable local document reports
// an error (not a false "no drift"), and that diffExitCode maps any error to
// >=2 regardless of the (zero-value) outcome. Spec test area: "diff exit
// codes with ... errors".
func TestComputeDiffOutcomeError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	local := []byte(`{not valid json or yaml: [`)
	live := []byte(`{"version":2,"organization":"acme","checks":[]}`)

	outcome, err := computeDiffOutcome(local, live, "config.yaml")
	r.Error(err)
	r.Contains(err.Error(), "config.yaml")

	code := diffExitCode(outcome, err)
	r.GreaterOrEqual(code, 2, "any compute error must exit >=2")
}

// TestDiffExitCode is a direct table test of the exit-code contract itself.
func TestDiffExitCode(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(0, diffExitCode(diffOutcome{Drift: false}, nil))
	r.Equal(1, diffExitCode(diffOutcome{Drift: true, Delta: "..."}, nil))
	r.GreaterOrEqual(diffExitCode(diffOutcome{}, errBoom), 2)
}

// TestDetectContentType covers the JSON/YAML extension inference reused by
// both `sp apply` and `sp checks import`.
func TestDetectContentType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("application/json", detectContentType("manifest.json"))
	r.Equal("application/json", detectContentType("MANIFEST.JSON"))
	r.Equal("application/yaml", detectContentType("manifest.yaml"))
	r.Equal("application/yaml", detectContentType("manifest.yml"))
	r.Equal("application/yaml", detectContentType("manifest"))
}

// TestPlanDriftCountsUnmanagedChecks is the regression guard for the worst
// answer this command could give.
//
// `unmanaged` says "this slug exists but no manifest owns it" — it says nothing
// about whether the file matches. For a FIRST-TIME organization every existing
// check is unmanaged, so treating unmanaged as agreement made
// `sp checks diff` print "No drift: config.yaml matches SolidPing" and exit 0
// for a file whose URLs, periods and labels all differed. An operator reads
// that as "the file is what is deployed", which is the single question this
// command exists to answer.
func TestPlanDriftCountsUnmanagedChecks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	firstTimeOrg := &applyResult{
		Unmanaged: 3,
		Plan: []applyPlanEntry{
			{Slug: "api", Action: "unmanaged", Changes: []checkFieldChange{
				{Field: "config.url", From: `"https://acme.com/v1"`, To: `"https://acme.com/v2"`},
			}},
			{Slug: "web", Action: "unmanaged"},
			{Slug: "db", Action: "unmanaged"},
		},
	}

	r.True(planDrift(firstTimeOrg),
		"an org whose checks are all unmanaged must not read as a match")
	r.Equal(1, planDiffExitCode(firstTimeOrg),
		"CI must fail on a file that does not match, whoever owns the checks")

	// The field diff survives onto unmanaged rows, so the operator is told
	// WHAT differs and not merely that something does.
	r.Len(firstTimeOrg.Plan[0].Changes, 1)
	r.Contains(changeSummary(&firstTimeOrg.Plan[0]), "config.url")
}

// TestPlanDriftOnAMatchingFile is the positive control: without it the
// assertion above would also pass if planDrift simply always said "drift",
// which would make the command useless in the other direction.
func TestPlanDriftOnAMatchingFile(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	matching := &applyResult{
		Unchanged: 12,
		Plan:      []applyPlanEntry{{Slug: "api", Action: "unchanged"}},
	}

	r.False(planDrift(matching))
	r.Equal(0, planDiffExitCode(matching))

	// Each counter on its own is drift — none of them may be forgotten.
	for name, res := range map[string]*applyResult{
		"created":   {Created: 1},
		"updated":   {Updated: 1},
		"deleted":   {Deleted: 1},
		"unmanaged": {Unmanaged: 1},
	} {
		r.Truef(planDrift(res), "%s must count as drift", name)
		r.Equalf(1, planDiffExitCode(res), "%s must exit 1", name)
	}
}
