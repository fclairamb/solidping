package checkemail

// SecretFields declares which top-level config keys carry secrets and must be
// split out of the public config column. Email (passive) has NONE.
//
// The `token` is deliberately NOT secret: it is the server-generated secret part
// of the check's public inbound address (`<token>@<addressDomain>`). Inbound
// email is matched to its check with `GetCheckByEmailToken`, which queries
// `config->>'token'` on the public column, and the dashboard renders the address
// from the same public field. Splitting/redacting it (as a declared secret
// would) removes it from the public column and breaks inbound matching — so it
// must stay public and queryable, like the webhook endpoint URL in the
// connection secret registry. The dashboard omits `config` entirely on
// passive-check edits, so the token is preserved across edits without the
// secret-merge.
//
// Kept as an explicit empty declaration (rather than dropping the interface) so
// this rationale is discoverable next to the other checkers' secret lists, and
// enforced by registry.TestNoUndeclaredCheckerSecrets's allowlist.
func (c *EmailConfig) SecretFields() []string {
	return []string{}
}

// ExportRedactedFields declares the config keys that stay in the PUBLIC column
// (see SecretFields above) but must never be written into a config-as-code
// export. Implements credentials.ExportRedactedFielder.
//
// The `token` is the local part of the check's inbound address
// (`<token>@<addressDomain>`): anyone who can read it can mail that address and
// mark the check `up`. Before spec 2026-09-11-02 the exporter stripped only
// SecretFields(), so a document stamped `secrets: stripped` carried the
// 48-hex-char token verbatim — and a real customer's git history got it.
//
// Storage is unchanged: the token stays public and queryable, so
// GetCheckByEmailToken and the dashboard's address rendering keep working. On
// import/apply an absent `token` is preserved from the existing check
// (checks.preserveAbsentRedactedFields); on a create the checker mints a fresh
// one, which is the only correct answer on a new instance anyway.
func (c *EmailConfig) ExportRedactedFields() []string {
	return []string{"token"}
}
