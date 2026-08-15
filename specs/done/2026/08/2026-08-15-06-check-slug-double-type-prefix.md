---
model: sonnet
effort: medium
---

# Auto-generated check slugs repeat the check type (`domain-domain-google-com`)

## Problem

When a check is created without a user-provided slug, the generated slug often
contains the check type twice. Creating a domain check for `google.com` yields
`domain-domain-google-com` instead of `domain-google-com`.

The duplication comes from two layers both claiming ownership of the type
prefix:

1. Most checkers already embed their type (or a short alias) in the default
   slug they build inside `Validate` — e.g.
   `server/internal/checkers/checkdomain/checker.go:65`
   (`spec.Slug = "domain-" + cfg.Domain`), and likewise `ftp-`, `redis-`,
   `ssl-`, `mongodb-`, `kafka-`, `smtp-`, `snmp-`, `oracle-`, `mssql-`,
   `mysql-`, `postgresql-`, `rabbitmq-`, `sftp-`, `rdp-`, `ntp-`, `imap-`,
   `pop3-`, `grpc-`, `clickhouse-`, `a2s-`, `mc-`, `docker-`, `ws-`, `k8s-`,
   `freebox-line-`, `sleep-`, plus the constant slugs `email`, `heartbeat`,
   `js-script` (grep `spec.Slug = ` under `server/internal/checkers/*/`).
2. The create path then unconditionally prepends the type again at
   `server/internal/handlers/checks/service.go:1250`:
   `spec.Slug = string(checker.Type()) + "-" + spec.Slug` (when the slug was
   not user-provided).

Resulting slugs today:

- `domain` → `domain-domain-google-com` (reported bug)
- `redis` → `redis-redis-myhost`, `ssl` → `ssl-ssl-example-com`, etc.
- `email` → `email-email`, `heartbeat` → `heartbeat-heartbeat`
- checkers using a short alias get a double *different* prefix:
  `websocket` → `websocket-ws-host`, `kubernetes` → `kubernetes-k8s-ns-name`,
  `postgres` → `postgres-postgresql-host`

Only the three checkers that emit a bare host — `checkhttp`
(`checker.go:113`), `checkudp` (`checker.go:61`), `checkssh`
(`checker.go:72`) — actually rely on the service-side prefix to produce the
intended `http-google-com` shape.

## Proposal

Make the checker the single owner of the default slug, and delete the
service-side prefixing:

1. Remove the `string(checker.Type()) + "-"` prepend at
   `server/internal/handlers/checks/service.go:1248-1251` (keep the
   `ensureUniqueSlug` / sanitize / truncate steps that follow).
2. Update the three bare-host checkers to include their own prefix so their
   generated slugs are unchanged:
   - `checkhttp`: `"http-" + hostname` (dots already replaced)
   - `checkudp`: `"udp-" + host`
   - `checkssh`: `"ssh-" + host`
3. Leave every other checker's prefix as-is — the short aliases (`ws-`,
   `k8s-`, `mc-`, `postgresql-`…) become the actual slug prefix, which is the
   nicer outcome (`ws-host`, not `websocket-ws-host`).
4. Audit checkers that never set `spec.Slug` (e.g. dns, icmp, tcp, ssl paths
   where `Validate` may leave it empty): after the change an empty slug must
   still get a sensible default rather than an empty/`x`-padded one. If any
   checker leaves `spec.Slug` empty, give it a proper default in that checker
   (`<type>-<target>` or the bare type name), mirroring the others.
5. Update tests that assert the current doubled or service-prefixed slugs
   (checker `Validate` tests and `handlers/checks` service tests), and add a
   regression test: creating a domain check for `google.com` with no slug
   produces `domain-google-com`.

Scope notes:

- Existing checks are untouched — slugs are stored at creation; this only
  changes newly generated defaults. No migration.
- User-provided slugs are unaffected (`userProvidedSlug` path already skips
  the prefixing).
- The manifest/apply and duplicate paths reuse the same create flow — verify
  they still behave (duplicate keeps its explicit `-copy` slug handling).
