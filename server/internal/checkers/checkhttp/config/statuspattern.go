package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sentinels for the `expectedStatusCodes` pattern grammar.
//
//nolint:gochecknoglobals // sentinel errors
var (
	errPatternEmpty    = errors.New("pattern cannot be empty")
	errInvalidWildcard = errors.New("invalid wildcard pattern: prefix must be 1-5")
	errInvalidPattern  = errors.New("pattern must be a number or wildcard like 2XX")
	errStatusCodeRange = errors.New("status code must be between 100 and 599")
)

const (
	// methodQuery is the IETF QUERY method (draft-ietf-httpbis-safe-method-w-body):
	// a safe, idempotent verb that carries a request body, like a cacheable POST.
	// net/http has no http.MethodQuery constant, so it is defined here.
	methodQuery = "QUERY"
)

// validateStatusPattern validates a single status code pattern.
// Valid patterns: exact codes like "200", "404", or wildcards like "2XX", "3XX".
func validateStatusPattern(pattern string) error {
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	if pattern == "" {
		return errPatternEmpty
	}

	// Check for wildcard pattern (e.g., "2XX")
	if strings.HasSuffix(pattern, "XX") && len(pattern) == 3 {
		prefix := pattern[0]
		if prefix >= '1' && prefix <= '5' {
			return nil
		}

		return fmt.Errorf("%w: %s", errInvalidWildcard, pattern)
	}

	// Check for exact status code
	code, err := strconv.Atoi(pattern)
	if err != nil {
		return fmt.Errorf("%w: %s", errInvalidPattern, pattern)
	}

	if code < 100 || code > 599 {
		return fmt.Errorf("%w: %d", errStatusCodeRange, code)
	}

	return nil
}
