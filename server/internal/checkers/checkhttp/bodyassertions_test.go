package checkhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// newTextBodyServer serves a fixed text/plain body, the shape an ASP.NET Core
// health endpoint returns.
func newTextBodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server
}

// naming the expected word at each call site.
//
//nolint:unparam // value is deliberately a parameter: the tests read better
func bodyEq(value string, ignoreCase bool) *AssertionNode {
	return &AssertionNode{
		Type:       NodeTypeAssertion,
		Operator:   opEq,
		Value:      value,
		IgnoreCase: ignoreCase,
	}
}

// TestAssertionNode_EvaluateBody covers every supported body operator with
// IgnoreCase both on and off, plus the whitespace rule.
func TestAssertionNode_EvaluateBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		node     *AssertionNode
		wantPass bool
	}{
		{"eq exact match", "HEALTHY", bodyEq("HEALTHY", false), true},
		{"eq is not a substring match", "UNHEALTHY", bodyEq("HEALTHY", false), false},
		{"eq case-sensitive mismatch", "Healthy", bodyEq("HEALTHY", false), false},
		{"eq ignore case", "Healthy", bodyEq("HEALTHY", true), true},
		{"eq ignore case rejects a different word", "Unhealthy", bodyEq("HEALTHY", true), false},
		{"eq trims a trailing newline", "HEALTHY\n", bodyEq("HEALTHY", false), true},
		{"eq trims surrounding whitespace", "  \tHealthy \r\n", bodyEq("HEALTHY", true), true},
		{"eq against an empty body fails", "", bodyEq("HEALTHY", true), false},

		{
			"neq passes on a different value", "DEGRADED",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNeq, Value: "HEALTHY"}, true,
		},
		{
			"neq fails on the same value", "HEALTHY",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNeq, Value: "HEALTHY"}, false,
		},
		{
			"neq ignore case fails on a folded match", "healthy",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNeq, Value: "HEALTHY", IgnoreCase: true}, false,
		},
		{
			"neq is also trimmed", "HEALTHY\n",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNeq, Value: "HEALTHY"}, false,
		},

		{
			"contains matches a substring", "service is HEALTHY now",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opContains, Value: "HEALTHY"}, true,
		},
		{
			"contains is case-sensitive by default", "service is Healthy now",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opContains, Value: "HEALTHY"}, false,
		},
		{
			"contains ignore case", "service is Healthy now",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opContains, Value: "HEALTHY", IgnoreCase: true}, true,
		},
		{
			// contains is NOT trimmed: the surrounding whitespace is part of
			// the subject, which is what lets an author assert on it.
			"contains sees untrimmed whitespace", "  HEALTHY  ",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opContains, Value: "  HEALTHY"}, true,
		},

		{
			"not_contains passes when absent", "HEALTHY",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNotContains, Value: "error"}, true,
		},
		{
			"not_contains fails when present", "internal Error",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNotContains, Value: "error", IgnoreCase: true}, false,
		},
		{
			"not_contains is case-sensitive by default", "internal Error",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNotContains, Value: "error"}, true,
		},

		{
			"regex matches", "Healthy",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "^Heal"}, true,
		},
		{
			"regex is case-sensitive by default", "Healthy",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "^HEAL"}, false,
		},
		{
			// Case folding must be applied as a parse flag, so an ANCHORED
			// pattern keeps its anchor — a "(?i)" string prefix is what this
			// case guards against regressing to.
			"regex ignore case keeps the anchor", "Healthy",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "^HEAL", IgnoreCase: true}, true,
		},
		{
			"regex ignore case still respects the anchor", "not Healthy",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "^HEAL", IgnoreCase: true}, false,
		},
		{
			// An inline (?-i) group must be able to turn folding back off,
			// which a string prefix cannot express.
			"regex inline (?-i) overrides the flag", "healthy",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "(?-i)HEALTHY", IgnoreCase: true}, false,
		},
		{
			"regex with its own inline flag is not corrupted", "healthy",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "(?i)HEALTHY"}, true,
		},
		{
			"invalid regex fails closed", "healthy",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "([unclosed"}, false,
		},

		{
			"and group, both pass", "HEALTHY",
			&AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Operator: opEq, Value: "healthy", IgnoreCase: true},
				{Type: NodeTypeAssertion, Operator: opNotContains, Value: "UN"},
			}}, true,
		},
		{
			"and group, one fails", "UNHEALTHY",
			&AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Operator: opContains, Value: "HEALTHY"},
				{Type: NodeTypeAssertion, Operator: opNotContains, Value: "UN"},
			}}, false,
		},
		{
			"or group, one passes", "DEGRADED",
			&AssertionNode{Type: NodeTypeOr, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Operator: opEq, Value: "HEALTHY", IgnoreCase: true},
				{Type: NodeTypeAssertion, Operator: opEq, Value: "DEGRADED", IgnoreCase: true},
			}}, true,
		},
		{
			"or group, none passes", "UNHEALTHY",
			&AssertionNode{Type: NodeTypeOr, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Operator: opEq, Value: "HEALTHY", IgnoreCase: true},
				{Type: NodeTypeAssertion, Operator: opEq, Value: "DEGRADED", IgnoreCase: true},
			}}, false,
		},

		{
			// Operators that cannot fail against a raw body are rejected at
			// evaluation time too, not silently passed.
			"exists is not evaluated against a body", "HEALTHY",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opExists}, false,
		},
		{
			"gt is not evaluated against a body", "42",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opGt, Value: "1"}, false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := tt.node.EvaluateBody(tt.body)
			require.Equal(t, tt.wantPass, result.Pass,
				"body %q operator %q ignoreCase=%v", tt.body, tt.node.Operator, tt.node.IgnoreCase)
		})
	}
}

// TestAssertionNode_EvaluateBody_ReportsActual pins that a failing assertion
// says what it expected AND what it got — a check that goes down with only
// "assertion failed" is not debuggable.
func TestAssertionNode_EvaluateBody_ReportsActual(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	result := bodyEq("HEALTHY", true).EvaluateBody("Unhealthy\n")
	r.False(result.Pass)
	r.Equal("HEALTHY", result.Expected)
	r.Equal("Unhealthy", result.Actual, "the trimmed subject is what was compared")
	r.True(result.IgnoreCase)
}

// TestAssertionNode_EvaluateBody_TruncatesActual keeps a 10 MB body out of the
// persisted result output.
func TestAssertionNode_EvaluateBody_TruncatesActual(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := strings.Repeat("a", maxAssertionActualRunes*3)
	result := bodyEq("HEALTHY", false).EvaluateBody(body)
	r.False(result.Pass)
	r.Len([]rune(result.Actual), maxAssertionActualRunes+1, "truncated plus the ellipsis")
	r.True(strings.HasSuffix(result.Actual, "…"))
}

// TestAssertionNode_ValidateBody rejects the operators that are meaningless
// against a raw body instead of accepting an assertion that could never fail.
func TestAssertionNode_ValidateBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		node    *AssertionNode
		wantErr bool
	}{
		{"eq is valid", bodyEq("HEALTHY", true), false},
		{
			"neq is valid",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNeq, Value: "x"}, false,
		},
		{
			"contains is valid",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opContains, Value: "x"}, false,
		},
		{
			"not_contains is valid",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNotContains, Value: "x"}, false,
		},
		{
			"regex is valid",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "^x"}, false,
		},
		{
			"no path is required",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opEq, Value: "x"}, false,
		},
		{
			"exists is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opExists}, true,
		},
		{
			"not_exists is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opNotExists}, true,
		},
		{
			"gt is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opGt, Value: "1"}, true,
		},
		{
			"gte is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opGte, Value: "1"}, true,
		},
		{
			"lt is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opLt, Value: "1"}, true,
		},
		{
			"lte is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opLte, Value: "1"}, true,
		},
		{
			"an unknown operator is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: "starts_with", Value: "x"}, true,
		},
		{
			"an empty value is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opEq}, true,
		},
		{
			"an invalid regex is rejected",
			&AssertionNode{Type: NodeTypeAssertion, Operator: opRegex, Value: "([unclosed"}, true,
		},
		{
			"an empty group is rejected",
			&AssertionNode{Type: NodeTypeAnd}, true,
		},
		{
			"a group with an invalid child is rejected",
			&AssertionNode{Type: NodeTypeAnd, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Operator: opExists},
			}}, true,
		},
		{
			"a valid group passes",
			&AssertionNode{Type: NodeTypeOr, Children: []AssertionNode{
				{Type: NodeTypeAssertion, Operator: opEq, Value: "HEALTHY"},
			}}, false,
		},
		{
			"an unknown node type is rejected",
			&AssertionNode{Type: "nope"}, true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.node.ValidateBody()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestHTTPChecker_Validate_BodyAssertions pins that an invalid body assertion
// is a config error rather than a silently-accepted assertion.
func TestHTTPChecker_Validate_BodyAssertions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	checker := &HTTPChecker{}

	specFor := func(node *AssertionNode) *checkerdef.CheckSpec {
		return &checkerdef.CheckSpec{
			Config: (&HTTPConfig{
				URL:            "https://acme.com/health",
				BodyAssertions: node,
			}).GetConfig(),
		}
	}

	r.NoError(checker.Validate(specFor(bodyEq("HEALTHY", true))))

	for _, operator := range []string{opExists, opNotExists, opGt, opGte, opLt, opLte} {
		err := checker.Validate(specFor(&AssertionNode{
			Type: NodeTypeAssertion, Operator: operator, Value: "1",
		}))
		r.Error(err, "operator %q must be rejected on a body assertion", operator)
		r.Contains(err.Error(), "bodyAssertions")
	}
}

// TestHTTPChecker_Execute_BodyAssertionsWithoutBodyMatchers is THE
// negative control for the response-body read gate.
//
// `bodyDrivesAssertions` (checker.go) decides whether the response body is
// read at all. A new body-reading assertion that is not added to that list
// fails OPEN: the body is never read, the assertion evaluates against "" and,
// worse, would be skipped entirely — the check reports UP whatever the
// endpoint returns. That exact bug shipped once for json_path_assertions
// (spec 2026-08-20-04).
//
// The cases below configure ONLY bodyAssertions, with no body_* key to open
// the gate on the side. The DOWN cases are what break if the gate is not
// extended; the UP cases are the controls that prove a green verdict means
// the assertion ran and passed rather than that it was skipped.
func TestHTTPChecker_Execute_BodyAssertionsWithoutBodyMatchers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		node       *AssertionNode
		wantStatus checkerdef.Status
	}{
		{
			// The headline case from the report: an ASP.NET Core health
			// endpoint returning a bare word with a 200. Status-code-only
			// monitoring calls this UP.
			name:       "unhealthy body, assertion alone, must be DOWN",
			body:       "Unhealthy",
			node:       bodyEq("HEALTHY", true),
			wantStatus: checkerdef.StatusDown,
		},
		{
			name:       "healthy body, assertion alone, must be UP",
			body:       "Healthy",
			node:       bodyEq("HEALTHY", true),
			wantStatus: checkerdef.StatusUp,
		},
		{
			name:       "trailing newline does not break exact equality",
			body:       "HEALTHY\n",
			node:       bodyEq("HEALTHY", true),
			wantStatus: checkerdef.StatusUp,
		},
		{
			// The inversion body_expect would get wrong: a substring match of
			// "HEALTHY" succeeds against "UNHEALTHY".
			name:       "eq does not match the UNHEALTHY superstring",
			body:       "UNHEALTHY",
			node:       bodyEq("HEALTHY", false),
			wantStatus: checkerdef.StatusDown,
		},
		{
			name:       "case matters when ignoreCase is off",
			body:       "Healthy",
			node:       bodyEq("HEALTHY", false),
			wantStatus: checkerdef.StatusDown,
		},
		{
			// An EMPTY body must still fail an eq assertion. The JSONPath
			// block is guarded by `respBody != ""`; the body-assertion block
			// deliberately is not, because an empty response is a meaningful
			// (and failing) subject for a text assertion.
			name:       "empty body fails an eq assertion",
			body:       "",
			node:       bodyEq("HEALTHY", true),
			wantStatus: checkerdef.StatusDown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			server := newTextBodyServer(t, tt.body)

			result := runCheck(t, &HTTPConfig{URL: server.URL, BodyAssertions: tt.node})
			r.Equal(tt.wantStatus, result.Status, result.Output)

			if tt.wantStatus == checkerdef.StatusDown {
				r.Contains(result.Output[checkerdef.OutputKeyError], errBodyAssertionFailed)
				r.Contains(result.Output, outputKeyBodyAssertions,
					"a failed body assertion must report what it expected and what it got")
			} else {
				r.NotContains(result.Output, outputKeyBodyAssertions)
			}
		})
	}
}

// TestHTTPChecker_Execute_BodyAssertionsReportActual pins the debuggability
// requirement: the failure output carries the expected AND the actual value.
func TestHTTPChecker_Execute_BodyAssertionsReportActual(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	server := newTextBodyServer(t, "Degraded\n")

	result := runCheck(t, &HTTPConfig{URL: server.URL, BodyAssertions: bodyEq("HEALTHY", true)})
	r.Equal(checkerdef.StatusDown, result.Status)

	assertionResult, ok := result.Output[outputKeyBodyAssertions].(AssertionResult)
	r.True(ok, "output must carry the evaluated assertion tree")
	r.False(assertionResult.Pass)
	r.Equal("HEALTHY", assertionResult.Expected)
	r.Equal("Degraded", assertionResult.Actual)
}

// TestHTTPConfig_BodyAssertions_RoundTrip pins that the key survives
// GetConfig -> FromMap with its ignoreCase flag intact, and that the emitted
// spelling is the canonical camelCase one.
func TestHTTPConfig_BodyAssertions_RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	original := &HTTPConfig{
		URL: "https://acme.com/health",
		BodyAssertions: &AssertionNode{
			Type: NodeTypeAnd,
			Children: []AssertionNode{
				{Type: NodeTypeAssertion, Operator: opEq, Value: "HEALTHY", IgnoreCase: true},
				{Type: NodeTypeAssertion, Operator: opNotContains, Value: "error"},
			},
		},
	}

	cfgMap := original.GetConfig()
	r.Contains(cfgMap, "bodyAssertions")
	r.NotContains(cfgMap, "body_assertions")

	restored := &HTTPConfig{}
	r.NoError(restored.FromMap(cfgMap))
	r.Equal(original.BodyAssertions, restored.BodyAssertions)
	r.True(restored.BodyAssertions.Children[0].IgnoreCase)
	r.False(restored.BodyAssertions.Children[1].IgnoreCase)
}

// TestHTTPConfig_BodyAssertions_FromMapSpellings accepts both the canonical
// camelCase key and the snake_case alias, and both spellings of ignoreCase.
func TestHTTPConfig_BodyAssertions_FromMapSpellings(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, key := range []string{"bodyAssertions", "body_assertions"} {
		for _, flag := range []string{"ignoreCase", "ignore_case"} {
			cfg := &HTTPConfig{}
			r.NoError(cfg.FromMap(map[string]any{
				"url": "https://acme.com/health",
				key: map[string]any{
					"type":     "assertion",
					"operator": "eq",
					"value":    "HEALTHY",
					flag:       true,
				},
			}))
			r.NotNil(cfg.BodyAssertions, "key %q", key)
			r.True(cfg.BodyAssertions.IgnoreCase, "key %q flag %q", key, flag)
		}
	}
}

// TestHTTPConfig_LegacyBodyMatchers_StillWork is the regression guard for
// spec item A.7: body_expect and friends keep working and keep round-tripping
// unchanged. They are load-bearing for the importers and for config-as-code
// files already in the wild.
func TestHTTPConfig_LegacyBodyMatchers_StillWork(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &HTTPConfig{}
	r.NoError(cfg.FromMap(map[string]any{
		"url":                 "https://acme.com/health",
		"body_expect":         "HEALTHY",
		"body_reject":         "panic",
		"body_pattern":        "^HEAL",
		"body_pattern_reject": "stack trace",
	}))
	r.Equal("HEALTHY", cfg.BodyExpect)
	r.Equal("panic", cfg.BodyReject)
	r.Equal("^HEAL", cfg.BodyPattern)
	r.Equal("stack trace", cfg.BodyPatternReject)

	restored := &HTTPConfig{}
	r.NoError(restored.FromMap(cfg.GetConfig()))
	r.Equal(cfg.BodyExpect, restored.BodyExpect)
	r.Equal(cfg.BodyReject, restored.BodyReject)
	r.Equal(cfg.BodyPattern, restored.BodyPattern)
	r.Equal(cfg.BodyPatternReject, restored.BodyPatternReject)

	// And they still drive a verdict: body_expect is a SUBSTRING match, which
	// is precisely why bodyAssertions had to exist.
	server := newTextBodyServer(t, "UNHEALTHY")
	result := runCheck(t, &HTTPConfig{URL: server.URL, BodyExpect: "HEALTHY"})
	r.Equal(checkerdef.StatusUp, result.Status,
		"body_expect stays a substring match — unchanged behavior")

	result = runCheck(t, &HTTPConfig{URL: server.URL, BodyExpect: "DEGRADED"})
	r.Equal(checkerdef.StatusDown, result.Status)
}

// TestAssertionNode_IgnoreCase_JSONPath pins that the flag also reaches
// JSONPath assertions, which could not match "OK" against "ok" before.
func TestAssertionNode_IgnoreCase_JSONPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		node     *AssertionNode
		wantPass bool
	}{
		{
			"case-sensitive mismatch", `{"status":"OK"}`,
			&AssertionNode{Type: NodeTypeAssertion, Path: "$.status", Operator: opEq, Value: "ok"}, false,
		},
		{
			"ignore case matches", `{"status":"OK"}`,
			&AssertionNode{
				Type: NodeTypeAssertion, Path: "$.status", Operator: opEq, Value: "ok", IgnoreCase: true,
			}, true,
		},
		{
			"not_contains works on JSONPath too", `{"status":"ok"}`,
			&AssertionNode{
				Type: NodeTypeAssertion, Path: "$.status", Operator: opNotContains, Value: "fail",
			}, true,
		},
		{
			"contains ignore case", `{"status":"Degraded"}`,
			&AssertionNode{
				Type: NodeTypeAssertion, Path: "$.status", Operator: opContains, Value: "DEGRADE", IgnoreCase: true,
			}, true,
		},
		{
			"regex ignore case", `{"status":"Degraded"}`,
			&AssertionNode{
				Type: NodeTypeAssertion, Path: "$.status", Operator: opRegex, Value: "^degraded$", IgnoreCase: true,
			}, true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			var data any
			r.NoError(json.Unmarshal([]byte(tt.body), &data))
			r.Equal(tt.wantPass, tt.node.Evaluate(data).Pass)
			r.NoError(tt.node.Validate())
		})
	}
}
