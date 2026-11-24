---
model: opus
effort: medium
---

# Bind sealed check secrets to the Unix socket target

## Problem

Found by the security scan of 2026-11-24 (finding AW4).

- **Severity:** Low.
- **Who can trigger it:** an org member with check-write access, on a check pinned to a private location whose agent allowlist (`checkers.unix_socket_paths`) admits more than one socket, typically a directory prefix.
- **Introduced by:** 410a7aea (spec 2026-11-24-01), not released yet (release PR #1005, v0.55.0, is open).

The browser-sealed secret binding of spec 2026-11-11-01 (AJ1) digests only the config keys a type declares in `TargetFields()`. The new socket keys are not among them:

| Type | `TargetFields()` | Socket key |
|---|---|---|
| http | `["url"]` (`server/internal/checkers/checkhttp/config/config.go:859-861`) | `unix_socket` |
| postgres | host, port, database (`checkpostgres/config/secret_fields.go:11-13`) | `socket` |
| mysql | host, port, database (`checkmysql/config/secret_fields.go:11-13`) | `socket` |
| mongodb | host, port, database (`checkmongodb/config/secret_fields.go:11-13`) | `socket` |
| redis | host, port (`checkredis/config/secret_fields.go:11-13`) | `socket` |
| smtp | host, port (`checksmtp/config/secret_fields.go:29-31`) | `socket` |

`TargetFieldsFor` (`server/internal/crypto/credentials/binding.go:101-109`) and the dash0 sealer, which gets its list from `handlers/checktypes/service.go`, agree on these lists.

Socket configs leave host and port empty, and an `http+unix` check keeps `url=http://localhost/...`. So a writer can do either of:

- move a sealed blob to another check that differs only by `socket`;
- edit only the `socket` of a sealed check.

The agent's `VerifyBinding` accepts the blob and sends the password to the other socket. The attacker still needs a listener on an allowlisted socket of the agent host, which is why this is Low. It is still a gap in the AJ1 guarantee that a sealed secret only reaches the target it was sealed for.

## Proposal

1. Add `socket` to `TargetFields()` of redis, postgres, mysql, mongodb and smtp, and `unix_socket` to http's. Keep the current lists as `LegacyTargetFields()` where a type has none, so blobs sealed before the change still verify through the legacy path (`LegacyTargetFieldsFor`, `binding.go:113-124`).
2. Check that the dash0 sealer reads the list from the check-types API rather than a hardcoded copy, so new blobs digest the socket key. Regenerate anything generated from `TargetFields`.
3. If the tcp check (`checktcp/config`) ever carries secrets, give it the same treatment. Today it declares no `TargetFields`, so `DefaultTargetFields` applies: confirm that this set includes `socket` or the wildcard.

## Tests

- `server/internal/crypto/credentials` binding test, per type: a blob sealed for `{socket: /run/app/a.sock}` fails `VerifyBinding` against `{socket: /run/app/b.sock}`. Same for http `unix_socket` with an unchanged `url`. It must fail on the current code.
- Positive control: the same blob verifies against the unchanged config, and a blob sealed before the change (the legacy field list) still verifies against its original config.

## To verify

- Whether a blob records the field list it was digested over (spec 2026-11-12-02) and the verifier honours that recorded list. If so, a blob digested without `socket` keeps verifying after a socket change. Then decide whether blobs without the socket key must be refused once a socket is set: recommended yes, for any config with a non-empty socket.
