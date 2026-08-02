---
model: sonnet
effort: medium
---

# Serve llms.txt at the server root (GitHub issue #183)

## Problem

[Issue #183](https://github.com/fclairamb/solidping/issues/183): we should expose
`llms.txt` at the root of the server, and generate it from the Docusaurus docs.

The generation half already exists: `web/docs/package.json:21` pulls in
`docusaurus-plugin-llms`, configured in `web/docs/docusaurus.config.ts:64-78` with
`generateLLMsTxt: true`, `generateLLMsFullTxt: true` and `generateMarkdownFiles: true`.
But because `baseUrl` is `/docs/` (`web/docs/docusaurus.config.ts:24`), the generated
files land at `/docs/llms.txt` / `/docs/llms-full.txt` — the conventional root-level
`/llms.txt` does not exist. Worse, an unmatched root path falls through to the SPA
catch-all (`server/internal/app/server.go:1336` → `serveAppStatic` at :1737), so
`GET /llms.txt` today silently returns the dash0 HTML shell instead of a 404 or the
manifest — actively misleading for LLM crawlers.

## Proposal

1. **Register root routes** `GET /llms.txt` and `GET /llms-full.txt` in the root-path
   block of `server/internal/app/server.go` (around :1319-1334, next to
   `/openapi.yaml`), **before** the `/*path` SPA catch-all at :1336. Serve the
   corresponding files from the embedded docs FS (`docsFiles`, embedded at
   `server/internal/app/server.go:147-148` from `internal/app/docsres/`), reusing the
   content-type/cache-header logic of `writeDocsFile` (:1634) or the `serveFile`
   helper used for `/openapi.yaml`.
2. **Verify link targets** inside the generated `llms.txt`: with `baseUrl: /docs/`
   the plugin should already emit `/docs/...`-prefixed (or absolute) URLs, so serving
   the file verbatim at the root is fine. If links turn out to be root-relative
   without the `/docs/` prefix, fix the plugin config (e.g. its `siteUrl`/path
   options) rather than rewriting at serve time.
3. Return 404 (not the SPA shell) if the file is missing from the embedded FS, e.g.
   in a build where docs weren't copied (`make copy-docs`, `Makefile:135`).
4. **Tests**: a handler test asserting `GET /llms.txt` returns 200 with
   `text/plain` content starting with the docs title, and that `/docs/llms.txt`
   still works.
5. Docs touch-up: mention `/llms.txt` in the docs-site section of `CLAUDE.md` /
   `wiki` if appropriate.

Non-goals: no robots.txt/sitemap work (nothing exists today — separate concern),
no change to the Docusaurus plugin output location.
