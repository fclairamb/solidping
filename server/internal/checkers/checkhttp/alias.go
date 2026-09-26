package checkhttp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhttp/config"

// HTTPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkhttp.HTTPConfig` call site compiling.
type HTTPConfig = checkconfig.HTTPConfig

// AssertionNode and the node-type constants are the body-assertion tree the
// config parses; aliased so this package's samples and checker keep their
// existing spelling.
type AssertionNode = checkconfig.AssertionNode

// AssertionNodeType and its kinds, aliased from the config.
type AssertionNodeType = checkconfig.AssertionNodeType

// Node kinds of an AssertionNode.
const (
	NodeTypeAssertion = checkconfig.NodeTypeAssertion
	NodeTypeAnd       = checkconfig.NodeTypeAnd
	NodeTypeOr        = checkconfig.NodeTypeOr
)

// Redirect host policy values, aliased from the config so the checker and
// this package's tests keep the short name.
const (
	RedirectHostPolicyAny      = checkconfig.RedirectHostPolicyAny
	RedirectHostPolicySameHost = checkconfig.RedirectHostPolicySameHost
)

// MatchStatusCode reports whether a status code satisfies any of the
// `expectedStatusCodes` patterns. It forwards to the config sub-package that
// owns the grammar.
func MatchStatusCode(actual int, patterns []string) bool {
	return checkconfig.MatchStatusCode(actual, patterns)
}

// RedactURL strips userinfo credentials from a URL for display. It forwards to
// the config sub-package that owns the redaction, keeping the existing
// `checkhttp.RedactURL` spelling for the chat integrations.
func RedactURL(rawURL string) string { return checkconfig.RedactURL(rawURL) }

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	opEq          = checkconfig.OpEq
	opGt          = checkconfig.OpGt
	opGte         = checkconfig.OpGte
	opLt          = checkconfig.OpLt
	opLte         = checkconfig.OpLte
	opNeq         = checkconfig.OpNeq
	opContains    = checkconfig.OpContains
	opNotContains = checkconfig.OpNotContains
	opRegex       = checkconfig.OpRegex
	opExists      = checkconfig.OpExists
	opNotExists   = checkconfig.OpNotExists
)

// Defined with the config; aliased so this package's call sites keep the short name.
const (
	maxAssertionActualRunes = checkconfig.MaxAssertionActualRunes
)

// AssertionResult is the evaluated assertion tree the config's evaluator
// produces; aliased so this package's tests and checker keep the short name.
type AssertionResult = checkconfig.AssertionResult
