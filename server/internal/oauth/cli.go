package oauth

// Well-known identifiers for the first-party SolidPing CLI OAuth client. The CLI
// (server/pkg/cli) opens the system browser at /authorize with this client_id
// and spins up an ephemeral loopback listener for the redirect. The client is
// seeded idempotently at server startup (see app.SeedCLIOAuthClient) so no
// dynamic registration is needed for the CLI.
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
