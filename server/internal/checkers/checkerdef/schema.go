package checkerdef

// The two interfaces below let a config declare, next to its own `Validate()`,
// the rules that reflection over the struct cannot see. The JSON Schema
// generator (server/gen/checkerschema) picks them up; nothing at runtime reads
// them.
//
// They exist so a cross-field rule is documented in the same file as the code
// that enforces it. A note kept in the generator instead would be a second
// place to remember, and the whole point of generating the schemas is that
// there is only one.

// SchemaNoter is implemented by a config whose `Validate()` enforces something
// a reflected JSON Schema cannot express — a cross-field rule, a format the
// schema deliberately does not claim, an `omitempty` field the validator still
// requires.
//
// Each returned string lands verbatim in the generated schema's
// `x-solidping-notes` array and in its `description`. They are documentation,
// not constraints: the Go validator stays the only authority.
type SchemaNoter interface {
	// SchemaNotes returns human-readable notes about rules the schema cannot
	// encode. Return nil when there are none.
	SchemaNotes() []string
}

// SchemaExclusiveGrouper is implemented by a config that requires exactly one
// of a set of keys — sftp's `password` xor `private_key`, for instance.
//
// The generator turns each group into a JSON Schema `oneOf` over
// single-key `required` branches, which encodes "exactly one of these is
// present": zero matching branches fails, two matching branches fails too.
// This is the one cross-field shape with a clean schema encoding, so it gets
// one; everything else goes through SchemaNotes.
type SchemaExclusiveGrouper interface {
	// SchemaExclusiveGroups returns groups of mutually exclusive, collectively
	// required config keys (json tag names). Return nil when there are none.
	SchemaExclusiveGroups() [][]string
}
