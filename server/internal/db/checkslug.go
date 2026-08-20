package db

import "strings"

// CheckSlugIndex is the partial unique index on (organization_uid, slug) that
// guarantees per-organization check slugs. Named here rather than only in the
// migrations because the create path has to recognize a violation OF THIS INDEX
// in a driver error string.
const CheckSlugIndex = "checks_slug_idx"

// sqliteCheckSlugColumns is how SQLite words the same violation. It names the
// COLUMNS, not the index ("UNIQUE constraint failed: checks.organization_uid,
// checks.slug"), so the index name alone would never match there.
const sqliteCheckSlugColumns = "checks.slug"

// IsCheckSlugCollision narrows IsUniqueViolation to the ONE constraint that
// re-resolving an auto-generated slug can actually resolve by trying again.
//
// Exported so a test can feed it a violation produced by a REAL database on
// each engine: it matches on driver error text, and driver error text is not
// something a hand-written fake can be trusted to reproduce.
//
// The narrowing matters: creating a check also inserts its check_jobs and an
// initial result in the same transaction, and a unique violation from any of
// those is not fixed by picking another slug — retrying it would just repeat
// the same failure a bounded number of times before surfacing it anyway.
func IsCheckSlugCollision(err error) bool {
	if !IsUniqueViolation(err) {
		return false
	}

	msg := err.Error()

	// Postgres names the index; SQLite names the columns.
	return strings.Contains(msg, CheckSlugIndex) ||
		strings.Contains(msg, sqliteCheckSlugColumns)
}
