# Publish auto-generated JSON Schemas for every check type's config

## Context

A SolidPing config-as-code manifest carries, per check, a free-form `config`
object whose keys depend on the check type (`url` for http, `clusterUid` +
`namespace` + `kind` + `name` for kubernetes, …). Today that shape lives only
in prose (wiki/conventions/checker-config.md) and in the Go validator code
(each checker's `XConfig` struct + `Validate()`); the OpenAPI spec describes
`config` as an open object — "fields accepted depend on type". Consequences:

- No editor autocomplete or inline validation while authoring a manifest.
- Third-party tooling (CI linters, generators, dashboards) has no
  machine-readable description of what a `kubernetes` or `oracle` check
  config accepts, short of reading Go source.
- The `/checks/validate` endpoint answers after the fact; it cannot help an
  editor as you type.

The Go structs are the single source of truth for what a config accepts
(and must stay so — see the parity guarantees in
[2026-09-22-01-sp-cli-size-gzip-and-config-decoupling.md](2026-09-22-01-sp-cli-size-gzip-and-config-decoupling.md)).
So the schemas should be **generated from those same structs**, not authored
by hand. JSON Schema generated via reflection over the `XConfig` types
(e.g. [invopop/jsonschema](https://github.com/invopop/jsonschema), mirroring
each struct's json tags) gives one derivation: change a config field in Go,
regenerate, and every consumer sees it.

### Relationship to the config/implementation decoupling

The decoupling spec moves every `XConfig` into a light `config/`
sub-package. That makes embedding the generated schemas into `sp` (and the
dashboard) natural — a few hundred KB of JSON — and gives the generator a
single import surface. But generation itself is a **build-time** step
(`go:generate`, output committed), so the generator may import today's
checker packages freely; this spec is *not* blocked by the decoupling. The
one ordering constraint: whichever package set the generator reflects over
must be the same one the validators run from, which is trivially true before
the split and enforced by the light registry after it.

## Design

1. **Generator**: a `go:generate` tool (server/gen/checkerschema or similar)
   that reflects over every `XConfig` type through the registry — the same
   enumeration the registry's `ParseConfig` switch already performs, so no
   check type can be forgotten (a registry test asserts one schema file per
   known type, both directions). Output: one JSON Schema draft 2020-12 file
   per check type, `server/internal/checkers/schemas/<type>.json`,
   regenerated and committed in the same PR as the struct change that
   caused it.

2. **Exposure**:
   - **API**: a read-only route, `GET /api/v1/checks/schema/{type}`, serving
     the generated schema with a proper content type, plus the schema list
     in the OpenAPI document (`oneOf` over the per-type schemas on the
     `config` property where tooling supports it, or a `$ref` catalog entry
     where it does not).
   - **Artifacts**: the same files attached to releases (like `sp` itself)
     so non-API consumers get a stable URL.

3. **Fidelity rules for the generation**:
   - json tags are the field names; `omitempty` does NOT mean optional in the
     schema unless the validator treats it so — where the two disagree, the
     struct's `Validate()` wins and the gap is recorded in a per-type
     `x-solidping-notes` extension rather than hand-editing the schema.
   - Secret fields (declared via `SecretFields()`) get
     `"format": "solidping-secret-ref"` and a description pointing at
     `${env:}/${param:}` references, so tools can nudge authors away from
     inlined credentials — matching what `validateNoInlinedCredentials`
     enforces.
   - Cross-field rules the reflection cannot express (sftp's
     password-or-private-key, sip's register-mode password) are documented
     in the schema's `description` and, where a clean encoding exists,
     `allOf`/`anyOf` hints — but the canonical enforcement remains the Go
     validator.

4. **Never a validator**: the schemas are a machine-readable *description*
   for editors and tools. `sp checks validate` and the server keep running
   the Go `Validate()` path unchanged — the CLI-on-server-code-path
   guarantee and the `ConfigError{Parameter}` contracts are not re-implemented
   in schema land. A doc note on each schema (and in the route description)
   says exactly that, so nobody wires a CI gate onto the JSON instead of
   `sp checks validate`.

## Verification

- Generator idempotence: running it twice produces byte-identical output;
  CI fails if the committed schemas are stale (a `go generate` diff check in
  the Dash0/backend lint job).
- A schema exists for every registry type and references only types the
  registry knows (pinned by a test, both directions).
- Spot-check parity on two checkers with the trickiest configs (kubernetes:
  enum of kinds; sftp: the cross-field note) — a config the Go validator
  rejects must also violate the schema for every constraint the schema
  claims to encode; where it does not, the gap must be listed in
  `x-solidping-notes`.
- The route is covered by a handler test and appears in the OpenAPI document.

## Decision

Generate per-type JSON Schemas from the same `XConfig` structs the Go
validators run on, commit them next to the code, expose them via
`GET /checks/schema/{type}` and release artifacts, and treat them strictly as
editor/tooling descriptions — validation authority stays in Go.
