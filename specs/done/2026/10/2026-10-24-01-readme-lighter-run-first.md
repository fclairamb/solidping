---
model: sonnet
effort: low
---

# Make the README light and run-first: video on top, one checks table, more inspirations

## Problem
`README.md` is 359 lines. It explains the product, the architecture and development before a reader
can run it, and the check types are spread over eight tables (`README.md:125-197`) with no link to
the docs. The "Non-Goals" list (`README.md:343-345`) still says multi-step transactional
testing is out of scope, but that is now supported. "Inspiration" (`README.md:347-349`) only
lists uptime-kuma.

## Proposal
1. Reorder `README.md` so the demo video block (`README.md:25-35`) comes right after the title and
   one-line tagline, before the badges. Keep it short; the badges follow.
2. Cut the README to what is needed to run it: tagline, video, "Try it" (docker run, first login
   and forced password change, brew/scoop/binary/Helm links), a 3-line config note (SQLite
   default, PostgreSQL for production, link to the configuration docs), a pointer to the docs for
   everything else. Move or drop the long sections (Overview/Key Features, Architecture, Project
   Structure, Development, CLI Client, Goals) by linking to `AGENTS.md`, `CONTRIBUTING.md` and
   `web/docs/docs/`. Keep Contributing and License. Target about 80 lines.
3. Replace the eight check-type tables (`README.md:125-197`) with one table
   (Type | What it checks | Docs). Each Docs cell links to its anchor in
   `https://docs.solidping.io/features/check-types#<anchor>` (anchors are in
   `web/docs/docs/features/check-types.md`, e.g. `#httphttps`, `#tcp`, `#postgresql`,
   `#smtp`, `#grpc`, `#kafka`, `#prometheus-metric`). Check types without a section there
   (JavaScript, Browser, Heartbeat) link to their own page, e.g.
   `web/docs/docs/features/javascript-checks.md`. Count must still match the "44 check types" claim.
4. Remove the "multi-step transactional / scripted user-flow" non-goal. If a Non-Goals list
   survives, keep only the APM/RUM line.
5. Replace "Inspiration" with several entries, each one line saying what SolidPing took from it:
   uptime-kuma, Better Stack (https://betterstack.com), plus a few others
   (e.g. Gatus, UptimeRobot, Statuspage, Healthchecks.io). The docs already have migration guides
   (`web/docs/docs/features/migrate/from-*.md`): link them where relevant.

## Tests
- No code. Check by hand: every link in the new table resolves (`grep -o '(https\?://[^)]*)' README.md`
  then curl each, or run the docs link check if there is one); every check type from the old tables
  appears exactly once in the new one; no "transactional" or "multi-step" left in the non-goals.
- `make lint-agent-docs` still passes (README is not an AGENTS.md, but confirm nothing references
  removed README anchors: `grep -rn "README.md#" --include=*.md .`).

## To verify
- Whether anything (website repo, docs, badges) deep-links to a README anchor that this removes
  (`#default-credentials`, `#supported-check-types`).
- Whether multi-step flows are really shipped, and under which check type, so the Inspiration or
  table text names it correctly (the user says "as we did it").
- The docs anchors for the check types not listed in `check-types.md` headings.
