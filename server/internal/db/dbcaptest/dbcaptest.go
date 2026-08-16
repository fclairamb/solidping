// Package dbcaptest holds the one shared table of workers.capabilities values
// and the verdict the database must return for each of them.
//
// IT EXISTS SO THE TWO DIALECTS CANNOT DRIFT. Postgres enforces the shape with
// a regex CHECK over the array's text rendering; SQLite enforces it with a
// json_valid CHECK plus a pair of json_each triggers. Those are completely
// different mechanisms, and the whole point of spec 2026-08-16-02 is that a
// value one of them refuses must not be storable in the other. Two hand-written
// per-dialect case lists would silently diverge the first time someone edits
// one; one list imported by both dialect tests cannot.
//
// It is a plain (non-_test) package only because Go cannot import a _test file
// across packages; nothing in the server links it.
package dbcaptest

// Case is one candidate value and whether the database must accept it.
type Case struct {
	// Name identifies the case in test output.
	Name string
	// PostgresLiteral is the SQL literal for the `text[]` column.
	PostgresLiteral string
	// SQLiteLiteral is the SQL literal for the JSON-array `text` column.
	SQLiteLiteral string
	// Accepted is the verdict BOTH dialects must return.
	Accepted bool
}

// SharedCases are the values both dialects can represent, with the verdict both
// must agree on.
//
// Note that `{1}` is deliberately absent: in Postgres a `text[]` has no
// non-string elements, so `'{1}'` IS the perfectly legal slug "1" — it is not a
// value Postgres "refuses", it is one it cannot even express differently. The
// JSON-typed hazards it stands for are covered by SQLiteOnlyCases below, and
// the string "1" is covered here as an accepted value.
func SharedCases() []Case {
	return []Case{
		// --- accepted: the three states themselves ---
		{"null-is-unknown", "null", "null", true},
		{"empty-set-is-a-real-none", "'{}'", "'[]'", true},
		{"single-capability", "'{ipv6}'", `'["ipv6"]'`, true},
		{"full-set", "'{ipv4,ipv6}'", `'["ipv4","ipv6"]'`, true},

		// --- accepted: the full slug charset ---
		{"hyphen-and-digits", "'{a-b,c9}'", `'["a-b","c9"]'`, true},
		{"digits-only-name", "'{1}'", `'["1"]'`, true},
		{"prefix-is-not-a-duplicate", "'{a,ab,b}'", `'["a","ab","b"]'`, true},

		// --- rejected: duplicates (it is a SET) ---
		{"duplicate-adjacent", "'{ipv4,ipv4}'", `'["ipv4","ipv4"]'`, false},
		{"duplicate-separated", "'{ipv4,ipv6,ipv4}'", `'["ipv4","ipv6","ipv4"]'`, false},

		// --- rejected: degenerate elements ---
		{"empty-string-element", `'{""}'`, `'[""]'`, false},
		{"empty-string-among-others", `'{ipv4,""}'`, `'["ipv4",""]'`, false},
		{"null-element", "array['a', null]", `'["a",null]'`, false},

		// --- rejected: out of charset ---
		{"uppercase", "'{IPv6}'", `'["IPv6"]'`, false},
		{"underscore", "'{ip_v6}'", `'["ip_v6"]'`, false},
		{"dot", "'{tls.13}'", `'["tls.13"]'`, false},
		{"space", `'{" "}'`, `'[" "]'`, false},
		{"embedded-space", `'{"a b"}'`, `'["a b"]'`, false},
		{"separator-inside-a-name", `'{"a,b"}'`, `'["a,b"]'`, false},
		{"non-ascii", "'{ipv6é}'", `'["ipv6é"]'`, false},
	}
}

// PostgresOnlyCases are hazards only the Postgres representation admits.
func PostgresOnlyCases() []Case {
	return []Case{
		{"multi-dimensional-array", "'{{a},{b}}'", "", false},
	}
}

// SQLiteOnlyCases are hazards only the JSON representation admits: JSON has
// types, and a capability set is a set of STRINGS.
func SQLiteOnlyCases() []Case {
	return []Case{
		{"number-element", "", "'[1]'", false},
		{"boolean-element", "", "'[true]'", false},
		{"nested-array-element", "", "'[[]]'", false},
		{"object-element", "", `'[{"a":1}]'`, false},
		{"json-object-not-array", "", `'{"a":1}'`, false},
		{"json-string-not-array", "", `'"ipv6"'`, false},
		{"not-json-at-all", "", "'ipv6'", false},
	}
}
