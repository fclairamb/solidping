package credentials

// ExportRedactedFielder is an *optional* interface a checker config can
// implement to declare top-level config keys that are PUBLIC AT REST but must
// NEVER appear in a config-as-code export.
//
// It is deliberately a SEPARATE declaration from SecretFielder, because the two
// answer different questions:
//
//   - SecretFields() — "must this value be split out of the public `config`
//     column and encrypted?" A yes removes the key from the queryable column,
//     which breaks any feature that looks the check up BY that value.
//   - ExportRedactedFields() — "may this value be written into a document an
//     operator commits to git?" A yes says nothing about storage.
//
// The email ingest token is the case that forced the split (spec
// 2026-09-11-02): it must stay in the public column because inbound mail is
// matched with `config->>'token'`, and it must never be exported because
// whoever reads it can mail the address and mark the check up. Declaring it
// secret would have broken inbound matching; leaving it undeclared put a live
// credential in a file stamped `secrets: stripped`.
//
// Contract for a declared field: the export omits it, and the import/apply
// path RESTORES it — from the stored value on an update, or by re-deriving it
// (see checks.preserveAbsentRedactedFields / deriveRedactedFields). A field
// that cannot be restored must not be declared here, or a round-trip would
// silently destroy it.
//
// Implementations should return a stable, type-level list — not per-instance.
type ExportRedactedFielder interface {
	ExportRedactedFields() []string
}

// ExportRedactedFieldsFor returns the export-redacted keys for a config,
// defaulting to the empty list when the config does not implement
// ExportRedactedFielder. Same nil-on-absent contract as SecretFieldsFor.
func ExportRedactedFieldsFor(cfg any) []string {
	if cfg == nil {
		return nil
	}

	if rf, ok := cfg.(ExportRedactedFielder); ok {
		return rf.ExportRedactedFields()
	}

	return nil
}
