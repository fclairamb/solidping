---
model: opus
effort: high
pr: own
automerge: false
---

# Build the MongoDB check's client from structured options, never from a URI string

## Problem

From security advisory GHSA-38m7-jhhr-79xx.

- **Severity:** High.
- **Who can trigger it:** any org member who can create or edit a check. On SaaS that is anyone, since any user can create their own org.
- **Where:** `server/internal/checkers/checkmongodb/config/config.go:234-257` (`BuildURI`).

`BuildURI` puts `username`, `password` and `host` into `mongodb://%s:%s@%s` without escaping them, and `Validate` (`config.go:207-231`) puts no character rule on any of them. `checker.go:54-56` passes the string to `options.Client().ApplyURI`. A value that contains `@`, `/` or `?` moves the host and appends any URI option the driver knows. The options that matter (mongo-driver v2.10.1, `x/mongo/driver/auth/oidc.go`):

- **OIDC with `ENVIRONMENT:k8s`:** `authMechanism=MONGODB-OIDC` with `authMechanismProperties=ENVIRONMENT:k8s` makes the driver read a local token file and send it to the server in the URI. The file is the one named by `AZURE_FEDERATED_TOKEN_FILE`, else by `AWS_WEB_IDENTITY_TOKEN_FILE`, else the Kubernetes service-account token.
- **OIDC with `ENVIRONMENT:azure` or `gcp`:** the driver fetches a metadata-service token, for an audience the author picks, with its own HTTP client. That client is not the SolidPing egress guard.
- **`appname`:** an injected username can turn the rest of the string, password included, into the app name. The driver sends it to the server in its first `hello`. A writer can therefore send an existing check's masked password to their own server by editing only `username`.
  - `TargetFields()` (`config/secret_fields.go:11-13`) binds host, port and database, but not `username`. So the edit does not break a browser-sealed blob's binding either.
- **A percent-encoded `…sock` host:** the driver dials a Unix socket on its own, outside `checkers.unix_socket_paths`. That works on self-hosted servers and on agents. An enforcing egress guard refuses the dial.

Verified with the driver's own parser. The URI built from a username holding an injected `authMechanism` resolves to the attacker's host with `MONGODB-OIDC` / `ENVIRONMENT:k8s`, and `Validate` accepts the config.

**Correction to the backlog spec:** `specs/backlog/2026-10-16-19-mongodb-uri-escaping.md` claims "the dialer is always the guarded/tunnel one (so no egress bypass)". That is wrong for the token sources above: they are read from disk or fetched by the driver's own HTTP client, not dialed through the guard. This spec replaces the backlog one: move it to `specs/cancelled/` with a pointer here.

## Proposal

1. **Drop the URI.** In `checkmongodb/checker.go`, build the client with `options.Client().SetHosts([]string{net.JoinHostPort(host, port)})` and, when a username is set, `SetAuth(options.Credential{Username: …, Password: …, AuthSource: <database or "admin">})`. Delete `BuildURI` and fix its callers and tests (`grep -rn BuildURI server/`). The socket form keeps a placeholder host plus `SetDialer(LocalSocketDialer…)` and `SetDirect(true)`, as today.
2. **Pin the auth mechanism.** Leave `AuthMechanism` empty, so the server negotiates SCRAM, and never set `AuthMechanismProperties`. No config key may select OIDC, AWS, X.509 or GSSAPI. If a mechanism field is ever needed, it takes an allowlist of `SCRAM-SHA-1` and `SCRAM-SHA-256`.
3. **Validate the host.** In `config.go` `Validate`, refuse a `host` containing `/ ? @ % , #` or whitespace, with a config error on `host`. A hostname, an IPv4 address or a bracketless IPv6 literal stays valid. Leave username and password free-form: they no longer reach a parser.
4. **Bind the username.** Add `username` to `TargetFields()` in `config/secret_fields.go`, and keep the legacy target-field list if the binding code needs one for old blobs (`server/internal/crypto/credentials/binding.go`). A sealed password moved to another username then fails verification.
5. **Tidy up.** Cancel the backlog spec, and add a line to `web/docs/docs/features/check-types.md` (MongoDB) saying that special characters in credentials are supported.

## Tests

- `checkmongodb/config` (or `checkmongodb`) table test: for a username, a password and a host each carrying `@`, `/`, `?` and URI option names (`authMechanism`, `authMechanismProperties`, `appname`, `tlsCAFile`), build the client options the way the checker does, then call `Validate()` and assert:
  - hosts are exactly `[host:port]`;
  - `Auth.AuthMechanism` is empty and `Auth.AuthMechanismProperties` is empty;
  - `AppName` is unset;
  - the password equals the input byte for byte.

  It must fail on the current code.
- `Validate` refuses `host` values containing `/`, `?`, `@` and `%` (each case), and accepts `db.acme.com`, `10.0.0.5` and `::1`.
- Binding test: a sealed blob verified against config A fails once only `username` changes.
- Positive control: `{host: db.acme.com, username: monitor, password: "p@ss:w/rd?"}` produces hosts `[db.acme.com:27017]`, username `monitor` and the exact password, and the existing socket tests (`socket_test.go`) still pass.

## To verify

- Whether `options.Credential` with an empty `AuthMechanism` still allows SCRAM negotiation on MongoDB 4.0+ (it should: the driver uses `saslSupportedMechs`).
- Every other `ApplyURI` call in the repo (`grep -rn ApplyURI server/`), for the same pattern.
