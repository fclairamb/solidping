---
model: sonnet
effort: medium
---

# Build the MongoDB check URI with escaped credentials

> **CANCELLED 2026-11-24 — superseded by
> [`2026-11-24-07-mongodb-structured-client-options`](../todos/2026-11-24-07-mongodb-structured-client-options.md)**
> (GHSA-38m7-jhhr-79xx). Escaping the URI is replaced by building the client
> from structured options. This spec's claim that the dialer is always the
> guarded one, "so no egress bypass", was wrong: injected OIDC options make the
> driver read a local token file or fetch a metadata-service token with its own
> HTTP client.

**Status:** backlog. Split out of `specs/done/2026/10/2026-10-16-04-mysql-dsn-injection.md` (resolved open question 1).

## Problem
`checkmongodb/config/config.go` builds the connection URI with `fmt.Sprintf("mongodb://%s:%s@%s", c.Username, c.Password, hostPort)`. A crafted username or password can inject URI options or shift the host. It is lower severity than the MySQL case: the dialer is always the guarded/tunnel one (so no egress bypass) and MongoDB has no `LOCAL INFILE` equivalent. A password that legitimately contains `@`, `:` or `/` also breaks the check today.

## Proposal
Build the URI with `url.URL{Scheme: "mongodb", User: url.UserPassword(user, pass), Host: hostPort}` (or set credentials through `options.Credential` instead of the URI), so every field is escaped. No validation restriction on password characters.

## Tests
- A password containing `@`, `:`, `/`, `?` and `&` round-trips to the driver unchanged and does not alter the host or options.
- A username crafted to add `?authSource=...` or a second host has no effect on the parsed options.
