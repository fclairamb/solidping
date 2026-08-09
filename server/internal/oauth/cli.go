package oauth

// Well-known identifiers for the first-party SolidPing CLI OAuth client: a
// pre-registered public client with a loopback redirect, seeded idempotently at
// server startup (see app.SeedCLIOAuthClient) so no dynamic registration is
// needed.
//
// `sp auth login` no longer uses it: since spec 2026-08-08-02 the CLI logs in
// through the RFC 8628 device authorization flow (/api/v1/auth/device), which
// also works over SSH and on headless machines where a loopback redirect cannot
// reach the CLI. The registration is kept for any other native client that
// still drives the loopback authorization-code flow against /oauth/authorize.
const (
	// CLIClientID is the pre-registered public client_id used by `sp auth login`.
	CLIClientID = "solidping-cli"

	// CLIClientName is the human-readable name stored for the CLI client and
	// shown on the consent screen.
	CLIClientName = "SolidPing CLI"

	// CLIRedirectURI is the registered loopback redirect. Per RFC 8252 §7.3 the
	// port is ignored by RedirectURIAllowed, so this single registered entry
	// covers every ephemeral 127.0.0.1:<port>/callback the CLI binds.
	CLIRedirectURI = "http://127.0.0.1/callback"

	// CLICallbackPath is the path the CLI's loopback listener serves and the
	// path component the registered redirect matches on.
	CLICallbackPath = "/callback"
)
