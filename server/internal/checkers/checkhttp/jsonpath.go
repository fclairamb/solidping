package checkhttp

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ohler55/ojg/jp"
)

const (
	opGte       = "gte"
	opLte       = "lte"
	opNeq       = "neq"
	opContains  = "contains"
	opRegex     = "regex"
	opExists    = "exists"
	opNotExists = "not_exists"

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
type AssertionNode struct {
	Type     AssertionNodeType `json:"type"`
	Path     string            `json:"path,omitempty"`
	Operator string            `json:"operator,omitempty"`
	Value    string            `json:"value,omitempty"`
	Children []AssertionNode   `json:"children,omitempty"`
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
	Children []AssertionResult `json:"children,omitempty"`
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
		Type:     NodeTypeAssertion,
		Path:     n.Path,
		Operator: n.Operator,
		Expected: n.Value,
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
	if n.Operator == opExists {
		result.Pass = len(results) > 0
		if !result.Pass {
			result.Error = errPathNotFound
		}
		return result
	}

	if n.Operator == opNotExists {
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

	result.Pass = compareValues(n.Operator, actual, n.Value)
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

func compareValues(operator, actual, expected string) bool {
	switch operator {
	case "eq":
		return actual == expected
	case opNeq:
		return actual != expected
	case opContains:
		return strings.Contains(actual, expected)
	case opRegex:
		re, err := regexp.Compile(expected)
		if err != nil {
			return false
		}
		return re.MatchString(actual)
	case "gt", opGte, "lt", opLte:
		return compareNumeric(operator, actual, expected)
	default:
		return false
	}
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
	case "gt":
		return actualFloat > expectedFloat
	case opGte:
		return actualFloat >= expectedFloat
	case "lt":
		return actualFloat < expectedFloat
	case opLte:
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
		"eq": true, opNeq: true, "gt": true, opGte: true,
		"lt": true, opLte: true, opContains: true, opRegex: true,
		opExists: true, opNotExists: true,
	}

	if !validOps[n.Operator] {
		return fmt.Errorf("%w: %s", errInvalidOperator, n.Operator)
	}

	// exists/not_exists must have empty value
	if (n.Operator == opExists || n.Operator == opNotExists) && n.Value != "" {
		return errExistsNoValue
	}

	// Numeric operators must have parseable value
	if n.Operator == "gt" || n.Operator == opGte || n.Operator == "lt" || n.Operator == opLte {
		if _, err := strconv.ParseFloat(n.Value, 64); err != nil {
			return fmt.Errorf("%w: %s", errNumericValueRequired, n.Operator)
		}
	}

	// Regex must compile
	if n.Operator == opRegex {
		if _, err := regexp.Compile(n.Value); err != nil {
			return fmt.Errorf("%w: %w", errInvalidRegex, err)
		}
	}

	return nil
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
