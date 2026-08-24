package models

// MigrateRegionList rewrites a check's region list, replacing every occurrence
// of `from` with `to` while preserving order and de-duplicating: a check that
// already declares BOTH slugs (the half-migrated case) must not end up with
// `to` twice, because the resulting check_jobs would violate the unique
// (check_uid, region) index.
//
// The second return reports whether anything actually changed, so a caller can
// skip a no-op UPDATE — which is what makes the region migration idempotent.
//
// Pure and allocation-light on purpose: it lives on the model package so both
// SQL dialects can share one definition of "what the rewritten array is",
// rather than expressing it twice in dialect-specific SQL.
func MigrateRegionList(list []string, from, target string) ([]string, bool) {
	if len(list) == 0 {
		return list, false
	}

	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	changed := false

	for _, region := range list {
		next := region
		if region == from {
			next = target
			changed = true
		}

		if seen[next] {
			// Dropping a duplicate is itself a change worth persisting.
			changed = true

			continue
		}

		seen[next] = true

		out = append(out, next)
	}

	if !changed {
		return list, false
	}

	return out, true
}
