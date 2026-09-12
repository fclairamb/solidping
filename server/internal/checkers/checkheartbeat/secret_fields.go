package checkheartbeat

// SecretFields declares which top-level config keys carry secrets and must be
// split out of the public config column. Heartbeat has NONE.
//
// The `token` is deliberately NOT secret: it is a server-generated shared secret
// embedded in the public ping URL (`/api/v1/heartbeat/{org}/{id}?token=…`). The
// ping handler validates it by reading `check.Config["token"]` from the public
// column, and the dashboard renders the ping URL from the same public field.
// Splitting/redacting it (as a declared secret would) removes it from the public
// column and breaks both — so it must stay public and queryable, like the
// webhook endpoint URL in the connection secret registry. The dashboard omits
// `config` entirely on passive-check edits, so the token is preserved across
// edits without needing the secret-merge.
//
// Kept as an explicit empty declaration (rather than dropping the interface) so
// this rationale is discoverable next to the other checkers' secret lists, and
// enforced by registry.TestNoUndeclaredCheckerSecrets's allowlist.
func (c *HeartbeatConfig) SecretFields() []string {
	return []string{}
}

// ExportRedactedFields declares the config keys that stay in the PUBLIC column
// (see SecretFields above) but must never be written into a config-as-code
// export. Implements credentials.ExportRedactedFielder.
//
// The `token` is the shared secret embedded in the public ping URL: anyone who
// can read it can post a heartbeat and keep the check green. Storage is
// unchanged — the ping handler still reads it from the public column — and an
// import/apply that omits it preserves the stored value, which is the exact
// behaviour preserveHeartbeatToken gave this field before spec 2026-09-11-02
// generalized it (checks.preserveAbsentRedactedFields).
func (c *HeartbeatConfig) ExportRedactedFields() []string {
	return []string{"token"}
}
