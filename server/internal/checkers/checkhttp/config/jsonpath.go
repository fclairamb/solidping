package config

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"

	"github.com/ohler55/ojg/jp"
)

// Defaults, bounds and config keys this check's rules are expressed in. The
// exported ones are aliased by the parent checker package.
const (
	// OpEq is a default or bound the config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	OpEq = "eq"
	// OpGt is a default or bound the config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	OpGt = "gt"
	// OpGte is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	OpGte = "gte"
	// OpLt is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	OpLt = "lt"
	// OpLte is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	OpLte = "lte"
	// OpNeq is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	OpNeq = "neq"
	// OpContains is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	OpContains = "contains"
	// OpNotContains is a default or bound this config's rules are expressed in; it is
	// exported so the parent checker package can alias it.
	OpNotContains = "not_contains"
	OpRegex       = "regex"
	OpExists      = "exists"
	OpNotExists   = "not_exists"

	errPathNotFound = "path not found"
)

// AssertionNodeType represents the type of an assertion tree node.
type AssertionNodeType string

const (
	// NodeTypeAssertion is a leaf node that evaluates a single JSONPath condition.
	NodeTypeAssertion AssertionNodeType = "assertion"
	// NodeTypeAnd is a group node where all children must pass.
	NodeTypeAnd AssertionNodeType = "and"
	// NodeTypeOr is a group node where at least one child must pass.
	NodeTypeOr AssertionNodeType = "or"
)

// AssertionNode represents a node in the assertion AST.
//
// The same AST serves two subjects: a parsed JSON document (Evaluate, driven by
// Path) and the raw response body as a string (EvaluateBody, which ignores
// Path entirely — see bodyassertions.go).
type AssertionNode struct {
	Type     AssertionNodeType `json:"type"`
	Path     string            `json:"path,omitempty"`
	Operator string            `json:"operator,omitempty"`
	Value    string            `json:"value,omitempty"`
	// IgnoreCase folds case for the string operators (eq, neq, contains,
	// not_contains) and compiles `regex` with the case-folding flag. It
	// applies to JSONPath and body assertions alike. Omitted at its false
	// default so an untouched config never gains the key.
	IgnoreCase bool            `json:"ignoreCase,omitempty"`
	Children   []AssertionNode `json:"children,omitempty"`
}

// AssertionResult represents the evaluation result of an assertion node.
type AssertionResult struct {
	Type     AssertionNodeType `json:"type"`
	Pass     bool              `json:"pass"`
	Path     string            `json:"path,omitempty"`
	Operator string            `json:"operator,omitempty"`
	Expected string            `json:"expected,omitempty"`
	Actual   string            `json:"actual,omitempty"`
	Error    string            `json:"error,omitempty"`
	// IgnoreCase echoes the node's flag so the UI can explain why
	// "HEALTHY" matched "Healthy".
	IgnoreCase bool              `json:"ignoreCase,omitempty"`
	Children   []AssertionResult `json:"children,omitempty"`
}

// Evaluate recursively evaluates the assertion AST against parsed JSON data.
func (n *AssertionNode) Evaluate(data any) AssertionResult {
	switch n.Type {
	case NodeTypeAssertion:
		return n.evaluateLeaf(data)
	case NodeTypeAnd:
		return n.evaluateAnd(data)
	case NodeTypeOr:
		return n.evaluateOr(data)
	default:
		return AssertionResult{Type: n.Type, Pass: false, Error: "unknown node type"}
	}
}

func (n *AssertionNode) evaluateLeaf(data any) AssertionResult {
	result := AssertionResult{
		Type:       NodeTypeAssertion,
		Path:       n.Path,
		Operator:   n.Operator,
		Expected:   n.Value,
		IgnoreCase: n.IgnoreCase,
	}

	// Parse JSONPath
	expr, err := jp.ParseString(n.Path)
	if err != nil {
		result.Error = fmt.Sprintf("invalid JSONPath: %s", err)
		return result
	}

	// Execute query
	results := expr.Get(data)

	// Handle exists/not_exists operators
	if n.Operator == OpExists {
		result.Pass = len(results) > 0
		if !result.Pass {
			result.Error = errPathNotFound
		}
		return result
	}

	if n.Operator == OpNotExists {
		result.Pass = len(results) == 0
		if !result.Pass {
			result.Actual = fmt.Sprintf("%v", results[0])
			result.Error = "path exists but should not"
		}
		return result
	}

	// For comparison operators, path must exist
	if len(results) == 0 {
		result.Error = errPathNotFound
		return result
	}

	actual := fmt.Sprintf("%v", results[0])
	result.Actual = actual

	result.Pass = compareValues(n.Operator, actual, n.Value, n.IgnoreCase)
	return result
}

func (n *AssertionNode) evaluateAnd(data any) AssertionResult {
	result := AssertionResult{
		Type:     NodeTypeAnd,
		Pass:     true,
		Children: make([]AssertionResult, 0, len(n.Children)),
	}

	for i := range n.Children {
		childResult := n.Children[i].Evaluate(data)
		result.Children = append(result.Children, childResult)
		if !childResult.Pass {
			result.Pass = false
		}
	}

	return result
}

func (n *AssertionNode) evaluateOr(data any) AssertionResult {
	result := AssertionResult{
		Type:     NodeTypeOr,
		Pass:     false,
		Children: make([]AssertionResult, 0, len(n.Children)),
	}

	for i := range n.Children {
		childResult := n.Children[i].Evaluate(data)
		result.Children = append(result.Children, childResult)
		if childResult.Pass {
			result.Pass = true
		}
	}

	return result
}

func compareValues(operator, actual, expected string, ignoreCase bool) bool {
	switch operator {
	case OpEq:
		return stringsEqual(actual, expected, ignoreCase)
	case OpNeq:
		return !stringsEqual(actual, expected, ignoreCase)
	case OpContains:
		return stringsContain(actual, expected, ignoreCase)
	case OpNotContains:
		return !stringsContain(actual, expected, ignoreCase)
	case OpRegex:
		re, err := compileAssertionRegex(expected, ignoreCase)
		if err != nil {
			return false
		}
		return re.MatchString(actual)
	case OpGt, OpGte, OpLt, OpLte:
		return compareNumeric(operator, actual, expected)
	default:
		return false
	}
}

func stringsEqual(actual, expected string, ignoreCase bool) bool {
	if ignoreCase {
		return strings.EqualFold(actual, expected)
	}

	return actual == expected
}

func stringsContain(actual, expected string, ignoreCase bool) bool {
	if ignoreCase {
		return strings.Contains(strings.ToLower(actual), strings.ToLower(expected))
	}

	return strings.Contains(actual, expected)
}

// compileAssertionRegex compiles an assertion pattern, optionally case-folded.
//
// Case folding is applied as a PARSE-TIME FLAG (syntax.FoldCase), never by
// concatenating a `(?i)` prefix onto the pattern string. The prefix trick
// mangles any pattern that carries its own inline flag group or that the user
// meant to scope with `(?-i)`, and it silently changes the meaning of a
// pattern beginning with a flag group of its own. Parsing with the flag lets
// an inline `(?-i)` turn folding back off exactly where the author asked.
func compileAssertionRegex(pattern string, ignoreCase bool) (*regexp.Regexp, error) {
	if !ignoreCase {
		return regexp.Compile(pattern)
	}

	parsed, err := syntax.Parse(pattern, syntax.Perl|syntax.FoldCase)
	if err != nil {
		return nil, err
	}

	return regexp.Compile(parsed.String())
}

func compareNumeric(operator, actual, expected string) bool {
	actualFloat, err := strconv.ParseFloat(actual, 64)
	if err != nil {
		return false
	}

	expectedFloat, err := strconv.ParseFloat(expected, 64)
	if err != nil {
		return false
	}

	switch operator {
	case OpGt:
		return actualFloat > expectedFloat
	case OpGte:
		return actualFloat >= expectedFloat
	case OpLt:
		return actualFloat < expectedFloat
	case OpLte:
		return actualFloat <= expectedFloat
	default:
		return false
	}
}

var (
	errUnknownNodeType      = errors.New("unknown node type")
	errPathRequired         = errors.New("assertion path is required")
	errInvalidOperator      = errors.New("invalid operator")
	errExistsNoValue        = errors.New("exists/not_exists operator must not have a value")
	errNumericValueRequired = errors.New("numeric operator requires a numeric value")
	errInvalidRegex         = errors.New("invalid regex pattern")
	errEmptyGroup           = errors.New("group must have at least one child")
)

// Validate checks the assertion AST for structural correctness.
func (n *AssertionNode) Validate() error {
	switch n.Type {
	case NodeTypeAssertion:
		return n.validateLeaf()
	case NodeTypeAnd, NodeTypeOr:
		return n.validateGroup()
	default:
		return fmt.Errorf("%w: %s", errUnknownNodeType, n.Type)
	}
}

func (n *AssertionNode) validateLeaf() error {
	if n.Path == "" {
		return errPathRequired
	}

	validOps := map[string]bool{
		OpEq: true, OpNeq: true, OpGt: true, OpGte: true,
		OpLt: true, OpLte: true, OpContains: true, OpNotContains: true,
		OpRegex: true, OpExists: true, OpNotExists: true,
	}

	if !validOps[n.Operator] {
		return fmt.Errorf("%w: %s", errInvalidOperator, n.Operator)
	}

	// exists/not_exists must have empty value
	if (n.Operator == OpExists || n.Operator == OpNotExists) && n.Value != "" {
		return errExistsNoValue
	}

	// Numeric operators must have parseable value
	if isNumericOperator(n.Operator) {
		if _, err := strconv.ParseFloat(n.Value, 64); err != nil {
			return fmt.Errorf("%w: %s", errNumericValueRequired, n.Operator)
		}
	}

	// Regex must compile
	if n.Operator == OpRegex {
		if _, err := compileAssertionRegex(n.Value, n.IgnoreCase); err != nil {
			return fmt.Errorf("%w: %w", errInvalidRegex, err)
		}
	}

	return nil
}

// isNumericOperator reports whether the operator compares two numbers.
func isNumericOperator(operator string) bool {
	switch operator {
	case OpGt, OpGte, OpLt, OpLte:
		return true
	default:
		return false
	}
}

func (n *AssertionNode) validateGroup() error {
	if len(n.Children) == 0 {
		return fmt.Errorf("%w: %s", errEmptyGroup, n.Type)
	}

	for i := range n.Children {
		if err := n.Children[i].Validate(); err != nil {
			return err
		}
	}

	return nil
}
