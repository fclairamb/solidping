---
model: sonnet
effort: high
---

# `sp` CLI: first-class YAML config management (export/import/diff/validate) matching the checks-as-code workflow

## Problem

The reference checks-as-code workflow (Acme's
`~/code/acme/exp-devops/solidping/`, see its `README.md`) manages a whole
SolidPing organization from a single tracked `config.yaml` — which is exactly
SolidPing's own v2 export document (`{version, exportedAt, organization,
secrets, defaults, checks}`) rendered as YAML. Today that workflow cannot be
done with the `sp` client alone; it needs two custom Python scripts
(`solidping_config.py` for export/import/diff, `validate_config.py` for offline
CI validation) because the CLI has gaps:

- **Export is JSON-only.** `sp checks export`
  ([apply.go:213](server/pkg/cli/apply.go:213)) pretty-prints the JSON wire
  document; the server endpoint likewise only emits JSON
  ([handler.go:511](server/internal/handlers/checks/handler.go:511), with a
  `.json` `Content-Disposition`). The config-as-code file is YAML for
  readability and diffability, so today you need PyYAML to produce it.
- **Import works with YAML only by accident.** The server sniffs JSON vs YAML
  ([handler.go:563](server/internal/handlers/checks/handler.go:563)), but
  `sp checks import` ([apply.go:255](server/pkg/cli/apply.go:255)) sends the
  raw bytes without an extension-based `Content-Type` (unlike `sp apply`,
  which has `detectContentType`, [apply.go:54](server/pkg/cli/apply.go:54)),
  and its text output is a re-indented JSON blob rather than the concise
  `created=N updated=N skipped=N` summary (with non-zero exit on per-item
  errors) the workflow relies on.
- **No drift detection.** There is no `sp checks diff`: "is the tracked file
  still what SolidPing holds?" (ignore `exportedAt`, unified diff, exit 1 on
  drift) is what `solidping_config.py diff` provides and what CI uses
  post-merge.
- **No offline validation.** `validate_config.py` re-implements in Python what
  the server already knows how to validate in Go (document shape, supported
  versions, unique kebab-case slugs, per-type config keys, duration/label/
  region formats, no inlined credentials, `expectedStatusCodes` vs legacy
  `expectedStatus` exclusivity, dependency graph soundness — parents exist, no
  self-edges, no cycles). That duplication drifts as check types evolve.

Goal: `sp` should natively support managing a SolidPing org's config with a
simple YAML file — the whole export → edit → validate → dry-run → import →
re-export loop — so the Python scripts become unnecessary.

## Proposal

All work is in `server/pkg/cli/` (plus small `server/pkg/client/` additions);
the server API already supports everything needed.

1. **YAML export.** Extend `sp checks export` with `--format yaml|json`,
   defaulting from the `-o` file extension (`.yaml`/`.yml` → YAML; stdout and
   `.json` stay JSON). The transcode must preserve the server's wire key order
   — decode the JSON into `yaml.Node` (or equivalent), never through
   `map[string]interface{}` — so exports are stable (two exports of identical
   live state are byte-identical) and diff cleanly against files produced by
   the Python tooling. Byte-for-byte PyYAML parity is *not* required; a
   one-time cosmetic re-format when switching tools is acceptable, ordering
   churn on every export is not.
2. **First-class YAML import.** `sp checks import` sets the request
   `Content-Type` from the file extension (reuse `detectContentType`), and in
   text mode prints the `created/updated/skipped` summary plus per-item errors
   (`[index] slug: error`), exiting non-zero when any item failed — matching
   the Python script's contract so it can drop into existing automation.
3. **New `sp checks diff [file]`.** Fetches the live export, loads the local
   YAML/JSON file, strips `exportedAt` from both, renders each as normalized
   sorted-key JSON, and prints a unified diff (`<file>` vs `solidping
   (live)`). Exit 0 on no drift, 1 on drift, ≥2 on errors — CI-friendly.
4. **New `sp checks validate <file>` — offline, no token, no network.** Parse
   and validate the document using the same server-side code paths
   (`server/internal/handlers/checks` — the v2 parse/decode in
   [export_v2.go](server/internal/handlers/checks/export_v2.go) and the
   import-side validation); `server/pkg/cli` may import
   `server/internal/...` since both live under `server/`. Scope is the
   *generic* rules listed in the Problem section; org-specific convention
   rules in `validate_config.py` (stack-root dependency hierarchy, RabbitMQ
   per-env symmetry) stay out — they are the workflow's business rules, not
   the format's.
5. **Docs.** Document the config-as-code loop (export → edit → validate →
   `import --dry-run` → import → re-export & commit) in the CLI's help output
   and the Docusaurus docs (`web/docs/`), including the "import is an upsert
   keyed on slug and never deletes — always start from a fresh export" caveat
   (deletion is `sp apply --prune` territory, already shipped).

Tests: table-driven CLI tests for format inference, order-stable YAML
round-trip (export → parse → re-export identical), diff exit codes with and
without drift, import summary/exit-code contract, and validate against both a
known-good document (the exp-devops `config.yaml` shape) and documents that
violate each generic rule.

Open question: should the *server* export endpoint also learn
`Accept: application/yaml` / `?format=yaml`? Not required — client-side
transcoding fully covers the workflow — so out of scope unless it falls out
for free.

## Implementation Plan

All work lands in `server/pkg/cli/` (new `checks_yaml.go` for export/import/
diff, reusing the existing `sp checks validate` command for Proposal 4) plus
small, targeted additions under `server/internal/handlers/checks/` and
`server/pkg/client/`.

1. **Export `ParseManifest`** (was `parseManifest`, `server/internal/handlers/
   checks/apply.go`). No behavior change — just makes the JSON/YAML-sniffing,
   v1/v2-aware decoder callable from `sp checks validate`'s offline path
   without duplicating it. Updated the two call sites in `handler.go` and the
   internal test file. *(done)*

2. **Generic document validator** — new `server/internal/handlers/checks/
   validate_document.go`, `ValidateDocument(doc *ExportDocument)
   []DocumentIssue`. Pure function, no DB/network. Reuses server code where it
   exists (`isSupportedExportVersion`, `validateSlug`,
   `registry.GetChecker(...).Validate(...)` for per-type config keys,
   `models.CheckDependencyKind.IsValid`, `timeutils.Duration.Scan` for
   duration formats) and adds the remaining generic-only rules that have no
   server-side equivalent yet (no-inlined-credentials heuristic,
   expectedStatus/expectedStatusCodes exclusivity, label/region format,
   dependency graph soundness — parent exists in-document, no self-edge, no
   duplicate parent, valid kind, no cycle via DFS). Explicitly does **not**
   port `validate_stack_topology` / `validate_rabbitmq_dependencies` from the
   Python reference — those are org-specific conventions. Covered by
   `validate_document_test.go`: one "known-good" case (the reference
   workflow's document shape) plus one sub-test per generic rule violation,
   modeled directly on `test_validate_config.py`'s table. *(done)*

3. **`sp checks validate`, extended to accept a whole document.** The command
   name `sp checks validate <file>` proposed by this spec collides with an
   existing command: `sp checks validate [--file <f>]` already validates a
   *single* check definition against the live server (network, needs a
   token). Rather than rename or shadow it, `checksValidateAction` now peeks
   the parsed input: if it decodes to a mapping with a `checks` array (an
   export/manifest document), it runs the new offline `ValidateDocument` path
   (no `APIHelper.GetClient` call — no token, no network) and reports
   `[slug] message` per issue, exit 0/1. Otherwise it falls back to the
   existing single-check online validate, unchanged. This satisfies the
   proposal's UX (`sp checks validate config.yaml`) without breaking or
   renaming the existing command.

4. **YAML export**, `server/pkg/cli/checks_yaml.go`. `sp checks export` gets
   `--format yaml|json`; when omitted, infer from `-o`'s extension
   (`.yaml`/`.yml` → yaml; stdout or `.json` → json, matching today's
   behavior). YAML transcoding decodes the server's JSON bytes with
   `json.Decoder.Token()` (which yields object keys in wire order) directly
   into a `yaml.Node` tree — never through `map[string]interface{}` — so two
   exports of identical live state are byte-identical and a round-trip
   (export → parse → re-export) reproduces the same bytes. Covered by a
   table-driven test asserting order preservation across a document whose
   JSON key order deliberately doesn't match alphabetical order.

5. **YAML import.** `sp checks import` now sets the request `Content-Type`
   from the file extension via the existing `detectContentType` (previously
   only used by `sp apply`), and its `client.SolidPingClient.ImportChecks`
   gains a `contentType` parameter. Text-mode output changes from a
   re-indented JSON blob to `created=N updated=N skipped=N` plus
   `[index] slug: error` per failure, exiting non-zero when any item failed —
   matching `solidping_config.py do_import`'s contract.

6. **`sp checks diff [file]`.** Fetches the live export, loads the local
   file (YAML or JSON, sniffed the same way as import), strips `exportedAt`
   from both, decodes each into `map[string]any` and renders as
   `json.MarshalIndent` (Go's map marshaling sorts keys, matching the
   Python tool's `sort_keys=True`), then prints a `github.com/pmezard/
   go-difflib` unified diff (`<file>` vs `solidping (live)`). Exit 0 (no
   drift) / 1 (drift) / ≥2 (read/fetch/parse errors) — CI-friendly, matching
   `solidping_config.py do_diff`.

7. **Docs.** Update each new/changed command's `Usage`/flag help text in
   `commands.go`, and add a "config as code" page under `web/docs/` covering
   the export → edit → validate → `import --dry-run` → import → re-export
   loop, plus the upsert-keyed-on-slug-never-deletes caveat (prune is a
   separate, already-shipped `sp apply --prune` concern).

8. **QA gate.** `make build-backend lint-back test` (Go-only unless docs
   change, then also `make build-docs`), scoped to `./pkg/cli/...` and
   `./internal/handlers/checks/...` while iterating, full targets once at the
   end.
