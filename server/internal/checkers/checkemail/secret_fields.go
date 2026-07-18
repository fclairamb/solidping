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
