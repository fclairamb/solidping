package db

import "strings"

// LabelKeyConstraint is the Postgres CHECK constraint that enforces the label
// key rule, and LabelValueConstraint its value twin. Named here rather than
// only in the migrations because the write paths have to recognize a violation
// of them in a driver error string.
const (
	LabelKeyConstraint   = "labels_key_check"
	LabelValueConstraint = "labels_value_check"
)

// sqliteLabelCheckFragments are how SQLite words the same violations. It says
// "CHECK constraint failed: <name>" for a named constraint and, for the
// anonymous inline CHECKs the pre-021 baseline used, names the TABLE instead —
// hence the third fragment.
func sqliteLabelCheckFragments() []string {
	return []string{
		"CHECK constraint failed: " + LabelKeyConstraint,
		"CHECK constraint failed: " + LabelValueConstraint,
		"CHECK constraint failed: labels",
	}
}

// IsLabelConstraintViolation reports whether err is the database refusing a
// label key or value.
//
// It exists as defense in depth, not as the enforcement: every write path
// validates keys and values in Go before touching the database (spec
// 2026-09-10-01). But a rule that lives in two places can drift, and the cost
// of drift must not be a raw driver string with a SQLSTATE in it reaching an
// import report — so a residual violation is translated back into the same
// validation error the Go gate would have produced.
func IsLabelConstraintViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	// Postgres names the constraint; SQLite names the constraint or the table.
	if strings.Contains(msg, LabelKeyConstraint) || strings.Contains(msg, LabelValueConstraint) {
		return true
	}

	for _, fragment := range sqliteLabelCheckFragments() {
		if strings.Contains(msg, fragment) {
			return true
		}
	}

	return false
}
