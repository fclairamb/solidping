package config

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	// RedactedMarker replaces the reported value of a sensitive header.
	RedactedMarker = "[redacted]"

	errHeaderNotFound = "header not found"

	errUnknownNodeTypeMsg = "unknown node type"
)

var (
	errHeaderOperatorUnsupported = errors.New(
		"operator is not supported for header assertions " +
			"(use eq, neq, contains, not_contains, regex, exists or not_exists)",
	)
	errHeaderNameInvalid  = errors.New("header assertion path must be a valid header name")
	errHeaderValueMissing = errors.New("assertion value is required")
)

// ShouldRedactHeader reports whether a response header's value must never be
// stored or reported (credentials, sessions, token/key/secret-like names).
func ShouldRedactHeader(name string) bool {
	lower := strings.ToLower(name)

	switch lower {
	case "set-cookie", "authorization", "proxy-authenticate",
		"proxy-authorization", "www-authenticate":
		return true
	}

	for _, needle := range [...]string{"token", "key", "secret"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}

	return false
}

func headerOperatorAllowed(operator string) bool {
	switch operator {
	case OpEq, OpNeq, OpContains, OpNotContains, OpRegex, OpExists, OpNotExists:
		return true
	default:
		return false
	}
}

// isHeaderToken reports whether s is an RFC 9110 token (a valid field name).
func isHeaderToken(name string) bool {
	if name == "" {
		return false
	}

	for i := 0; i < len(name); i++ {
		c := name[i]
		isAlnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'

		if !isAlnum && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}

	return true
}

// ValidateHeaders checks a header-assertion AST. The leaf Path is the header name.
func (n *AssertionNode) ValidateHeaders() error {
	switch n.Type {
	case NodeTypeAssertion:
		return n.validateHeaderLeaf()
	case NodeTypeAnd, NodeTypeOr:
		if len(n.Children) == 0 {
			return fmt.Errorf("%w: %s", errEmptyGroup, n.Type)
		}

		for i := range n.Children {
			if err := n.Children[i].ValidateHeaders(); err != nil {
				return err
			}
		}

		return nil
	default:
		return fmt.Errorf("%w: %s", errUnknownNodeType, n.Type)
	}
}

func (n *AssertionNode) validateHeaderLeaf() error {
	if n.Path == "" {
		return errPathRequired
	}

	if !isHeaderToken(n.Path) {
		return fmt.Errorf("%w: %q", errHeaderNameInvalid, n.Path)
	}

	if !headerOperatorAllowed(n.Operator) {
		return fmt.Errorf("%w: %s", errHeaderOperatorUnsupported, n.Operator)
	}

	if n.Operator == OpExists || n.Operator == OpNotExists {
		if n.Value != "" {
			return errExistsNoValue
		}

		return nil
	}

	if n.Value == "" {
		return fmt.Errorf("%w: %s", errHeaderValueMissing, n.Operator)
	}

	if n.Operator == OpRegex {
		if _, err := compileAssertionRegex(n.Value, n.IgnoreCase); err != nil {
			return fmt.Errorf("%w: %w", errInvalidRegex, err)
		}
	}

	return nil
}

// EvaluateHeaders evaluates the assertion AST against response headers.
func (n *AssertionNode) EvaluateHeaders(headers http.Header) AssertionResult {
	switch n.Type {
	case NodeTypeAssertion:
		return n.evaluateHeaderLeaf(headers)
	case NodeTypeAnd, NodeTypeOr:
		requireAll := n.Type == NodeTypeAnd
		result := AssertionResult{
			Type:     n.Type,
			Pass:     requireAll,
			Children: make([]AssertionResult, 0, len(n.Children)),
		}

		for i := range n.Children {
			child := n.Children[i].EvaluateHeaders(headers)
			result.Children = append(result.Children, child)

			if requireAll && !child.Pass {
				result.Pass = false
			}

			if !requireAll && child.Pass {
				result.Pass = true
			}
		}

		return result
	default:
		return AssertionResult{Type: n.Type, Pass: false, Error: errUnknownNodeTypeMsg}
	}
}

func (n *AssertionNode) evaluateHeaderLeaf(headers http.Header) AssertionResult {
	result := AssertionResult{
		Type:       NodeTypeAssertion,
		Path:       n.Path,
		Operator:   n.Operator,
		Expected:   n.Value,
		IgnoreCase: n.IgnoreCase,
	}

	if !headerOperatorAllowed(n.Operator) {
		result.Error = fmt.Sprintf("%s: %s", errHeaderOperatorUnsupported.Error(), n.Operator)

		return result
	}

	values := headers.Values(n.Path)

	if len(values) > 0 {
		if ShouldRedactHeader(n.Path) {
			result.Actual = RedactedMarker
		} else {
			result.Actual = truncateActual(strings.Join(values, ", "))
		}
	}

	if n.Operator == OpRegex {
		if _, err := compileAssertionRegex(n.Value, n.IgnoreCase); err != nil {
			result.Error = fmt.Sprintf("invalid regex pattern: %s", err)

			return result
		}
	}

	n.applyHeaderOperator(&result, values)

	return result
}

// applyHeaderOperator sets Pass (and Error when the header is absent) from the
// header's lines.
func (n *AssertionNode) applyHeaderOperator(result *AssertionResult, values []string) {
	switch n.Operator {
	case OpExists:
		result.Pass = len(values) > 0
		if !result.Pass {
			result.Error = errHeaderNotFound
		}
	case OpNotExists:
		result.Pass = len(values) == 0
	case OpNeq, OpNotContains:
		// All-must-pass: no line may violate; an absent header passes.
		result.Pass = true

		for _, v := range values {
			if !compareValues(n.Operator, v, n.Value, n.IgnoreCase) {
				result.Pass = false

				break
			}
		}
	default:
		if len(values) == 0 {
			result.Error = errHeaderNotFound

			return
		}

		for _, v := range values {
			if compareValues(n.Operator, v, n.Value, n.IgnoreCase) {
				result.Pass = true

				break
			}
		}
	}
}
