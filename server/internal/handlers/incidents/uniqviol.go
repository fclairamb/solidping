package incidents

import "strings"

// isUniqueConstraintError reports whether the error message looks like a
// unique-constraint violation from either SQLite or PostgreSQL. It's a string
// match because the underlying drivers wrap errors differently and we only
// need to distinguish "duplicate row" from anything else; over-matching is
// safe because the caller always re-fetches.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "SQLSTATE 23505")
}
