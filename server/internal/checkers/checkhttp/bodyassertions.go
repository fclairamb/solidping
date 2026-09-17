package checkhttp

import (
	"errors"
	"fmt"
	"strings"
)

// maxAssertionActualRunes caps what a body assertion reports as its actual
// value. The body is read up to maxBodySize (10 MB) and the assertion result
// is persisted with the check result, so the whole body must never end up in
// there — 256 runes is enough to see a health word, a short error page banner
// or the start of an unexpected HTML response.
const maxAssertionActualRunes = 256

var (
	errBodyOperatorUnsupported = errors.New(
		"operator is not supported for body assertions (use eq, neq, contains, not_contains or regex)",
	)
	errBodyValueRequired = errors.New("assertion value is required")
)

// bodyOperatorAllowed reports whether an operator is meaningful against a raw
// response body. `exists` / `not_exists` and the numeric comparisons only make
// sense against a JSONPath query result: a raw body always "exists", so
// accepting them here would produce an assertion that can never fail — the
// same fail-open shape this whole feature exists to avoid.
func bodyOperatorAllowed(operator string) bool {
	switch operator {
	case opEq, opNeq, opContains, opNotContains, opRegex:
		return true
	default:
		return false
	}
}

// bodySubject returns the string an operator compares against.
//
// THE WHITESPACE RULE, in one place: `eq` and `neq` compare against a
// space-trimmed body, every other operator sees the body verbatim. A
// text/plain health endpoint almost always emits a trailing newline, so
// `eq "HEALTHY"` against "HEALTHY\n" failing would be a permanent support
// ticket; `contains` and `regex` on the other hand are explicitly about
// finding something inside the body, where trimming the ends would be
// surprising and where an author who cares can anchor with \s* themselves.
func bodySubject(operator, body string) string {
	if operator == opEq || operator == opNeq {
		return strings.TrimSpace(body)
	}

	return body
}

// truncateActual shortens a reported actual value to maxAssertionActualRunes.
func truncateActual(value string) string {
	runes := []rune(value)
	if len(runes) <= maxAssertionActualRunes {
		return value
	}

	return string(runes[:maxAssertionActualRunes]) + "…"
}

// EvaluateBody evaluates the assertion AST against the raw response body.
//
// Unlike Evaluate, which resolves each leaf's Path against a parsed JSON
// document, every leaf here shares one subject: the body itself. Path is
// ignored (and never required — see validateBodyLeaf).
func (n *AssertionNode) EvaluateBody(body string) AssertionResult {
	switch n.Type {
	case NodeTypeAssertion:
		return n.evaluateBodyLeaf(body)
	case NodeTypeAnd:
		return n.evaluateBodyGroup(body, true)
	case NodeTypeOr:
		return n.evaluateBodyGroup(body, false)
	default:
		return AssertionResult{Type: n.Type, Pass: false, Error: "unknown node type"}
	}
}

func (n *AssertionNode) evaluateBodyLeaf(body string) AssertionResult {
	result := AssertionResult{
		Type:       NodeTypeAssertion,
		Operator:   n.Operator,
		Expected:   n.Value,
		IgnoreCase: n.IgnoreCase,
	}

	if !bodyOperatorAllowed(n.Operator) {
		result.Error = fmt.Sprintf("%s: %s", errBodyOperatorUnsupported.Error(), n.Operator)

		return result
	}

	subject := bodySubject(n.Operator, body)
	result.Actual = truncateActual(subject)

	if n.Operator == opRegex {
		if _, err := compileAssertionRegex(n.Value, n.IgnoreCase); err != nil {
			result.Error = fmt.Sprintf("invalid regex pattern: %s", err)

			return result
		}
	}

	result.Pass = compareValues(n.Operator, subject, n.Value, n.IgnoreCase)

	return result
}

func (n *AssertionNode) evaluateBodyGroup(body string, requireAll bool) AssertionResult {
	nodeType := NodeTypeOr
	if requireAll {
		nodeType = NodeTypeAnd
	}

	result := AssertionResult{
		Type:     nodeType,
		Pass:     requireAll,
		Children: make([]AssertionResult, 0, len(n.Children)),
	}

	for i := range n.Children {
		childResult := n.Children[i].EvaluateBody(body)
		result.Children = append(result.Children, childResult)

		if requireAll && !childResult.Pass {
			result.Pass = false
		}

		if !requireAll && childResult.Pass {
			result.Pass = true
		}
	}

	return result
}

// ValidateBody checks a body-assertion AST for structural correctness. It is
// the body-subject counterpart of Validate: no Path is required, and the
// operators that are meaningless against a raw body are rejected outright
// rather than accepted into an assertion that could never fail.
func (n *AssertionNode) ValidateBody() error {
	switch n.Type {
	case NodeTypeAssertion:
		return n.validateBodyLeaf()
	case NodeTypeAnd, NodeTypeOr:
		return n.validateBodyGroup()
	default:
		return fmt.Errorf("%w: %s", errUnknownNodeType, n.Type)
	}
}

func (n *AssertionNode) validateBodyLeaf() error {
	if !bodyOperatorAllowed(n.Operator) {
		return fmt.Errorf("%w: %s", errBodyOperatorUnsupported, n.Operator)
	}

	if n.Value == "" {
		return fmt.Errorf("%w: %s", errBodyValueRequired, n.Operator)
	}

	if n.Operator == opRegex {
		if _, err := compileAssertionRegex(n.Value, n.IgnoreCase); err != nil {
			return fmt.Errorf("%w: %w", errInvalidRegex, err)
		}
	}

	return nil
}

func (n *AssertionNode) validateBodyGroup() error {
	if len(n.Children) == 0 {
		return fmt.Errorf("%w: %s", errEmptyGroup, n.Type)
	}

	for i := range n.Children {
		if err := n.Children[i].ValidateBody(); err != nil {
			return err
		}
	}

	return nil
}
