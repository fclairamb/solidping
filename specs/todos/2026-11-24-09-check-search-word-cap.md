---
model: sonnet
effort: medium
---

# Cap the words and the length of the check search `q`

## Problem

Found by the security scan of 2026-11-24 (finding AW3).

- **Severity:** Medium.
- **Who can trigger it:** any principal that can list checks: a viewer, a plain member, or an MCP token with read scope. On SaaS that is anyone.
- **Introduced by:** the multi-word search of 8d266776, first shipped in v0.54.1.

`applyCheckSearchFilter` (`server/internal/db/postgres/postgres.go:2059-2067`, `server/internal/db/sqlite/sqlite.go:2034-2042`) adds one `(LOWER(name) LIKE ? ESCAPE '\' OR LOWER(slug) LIKE ? ESCAPE '\')` conjunct per whitespace-separated word of `q`. `ListChecks` applies it to both the page query and the count query (`postgres.go:2227-2228`, `sqlite.go:2195-2196`). Nothing caps the word count or the length of `q`:

- REST: `server/internal/handlers/checks/handler.go:337` takes `query.Get("q")` raw.
- MCP: `server/internal/mcp/tools_checks.go:65` does the same for `list_checks`.
- The export path reuses the same filter.

The only bound is Go's default 1 MB request header limit (no `MaxHeaderBytes` is set). bun inlines the arguments client-side, so the PostgreSQL 65,535-parameter limit does not apply, and SQLite's variable limit does not either. One request of about 500,000 one-letter words therefore builds two SQL statements of tens of megabytes. That costs Go memory and CPU, then database parse, plan and per-row evaluation time, while holding one of the global concurrency slots. A handful of such requests degrades every tenant on the node and on the shared database. Before 8d266776, `q` produced a single `LIKE`. This is the same class as AE1 and AG1, which were fixed by capping before use.

## Proposal

1. Add one shared helper, for example `checkfilter.SearchTerms(q string) []string`, next to the existing checkUid caps in `server/internal/checkfilter`. It:
   - truncates `q` to 256 bytes on a rune boundary;
   - splits it with `strings.Fields`;
   - deduplicates the words;
   - keeps the first 8.
2. Call it in both dialects' `applyCheckSearchFilter`, so REST, MCP and export all go through it. Alternatively, apply it once where `opts.Query` is set (`handler.go:337`, `tools_checks.go:65`) and keep the DB helper as the backstop. Do both: the DB helper is the one place every caller passes through.
3. Optional: answer a `q` longer than 1 KiB with `400 VALIDATION_ERROR` on REST, so a client sees why its search was cut.

## Tests

- `checkfilter` unit test: 10,000 words yield exactly 8 terms. A 10 KiB single word is cut to 256 bytes without splitting a rune. Duplicates collapse.
- Postgres and SQLite DB tests (`server/internal/db/service_test.go`, which already covers multi-word search in 8d266776): a search with 5,000 words compiles to at most 8 `LIKE` pairs per statement. Assert it on the generated SQL string (`query.String()`) or by counting `LIKE` occurrences. It must fail on the current code.
- Positive control: a three-word search (`"acme api prod"`) still matches only the checks whose name or slug contains all three words, and an empty `q` still adds no clause.
