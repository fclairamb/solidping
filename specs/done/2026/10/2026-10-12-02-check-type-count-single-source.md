---
model: sonnet
effort: medium
---

# The advertised "N check types" comes from the registry, and a test fails when a doc disagrees

## Problem
The number of check types is written by hand in many places, and each place drifts on its own schedule.

**What the registry says.** `checkTypesRegistry` (`server/internal/checkers/checkerdef/types.go:418`) has 45 entries. `GetChecker` (`server/internal/checkers/registry/registry.go:75-170`) has 45 `case` arms, and `ListCheckTypes` (`types.go:516-564`) lists the same 45. Two of them are not counted in the public tally:
- `sleep` (`types.go:258-262`): synthetic load generator. Its comment says it "must not be counted in the customer \"N check types\" tally". Same rule in `server/internal/checkers/AGENTS.md:24-29` and `checksleep/checker.go:7`.
- `private-location` (`types.go:253-257`): the liveness monitor that is system-created with each private location. It is not written down as excluded anywhere, but the README has excluded it since it shipped: commit f1cce843b added it and the README stayed at 40.

So the advertised count today is **45 - 2 = 43**. The README's "43" is correct. Commit 881ca8d6c (crawl + health) moved it from 41 to 43, and its enumerated list (`README.md:58`) has exactly 43 items. The exclusion rule only lives in comments and in the README author's head, though. `docs_anchor_test.go:21` exempts only `sleep`, and `mcp/tools_checktypes.go:173` excludes only `sleep` too.

**Where the number is hard-coded in this repo** (verified with `git grep -niE "[0-9]{2}\+? *check[- ]?types?"`, outside `specs/` and `CHANGELOG.md`):

| File:line | Says | Right? |
|---|---|---|
| `README.md:8` | 43 check types | yes |
| `README.md:58` | **43 check types**: list | yes |
| `web/docs/docs/features/check-types.md:8` | **43 check types** | yes |
| `web/docs/docusaurus.config.ts:129` | 43 check types (site description) | yes |
| `web/docs/docs/intro.md:13` | **39 Check Types** | no |
| `wiki/architecture.md:163` | `# 39 check type implementations` | no (45 packages) |
| `wiki/competitors/comparison/monitor-types.md:36` | 40 check types | no |
| `wiki/competitors/comparison/overview.md:19` | 40 check types (39 registered internally), "counts refreshed 2026-07-22" | no |
| `wiki/competitors/comparison/solidping-position.md:110` | 40 check types | no |
| `wiki/competitors/positioning.md:126,169,240,260` | 40 check types | no |
| `wiki/competitors/pulsetic.md:66,257` | 40 check types | no |
| `wiki/competitors/indie-watch.md:105,176,290,384,497` | 40 check types | dated log entries, leave as is |

`server.json`, `deploy/mcpb/manifest.json` and `server/internal/app/openapi/llms.txt` carry no number. The MCP tool descriptions derive their type list from the registry already (`allowedCheckTypes`, `mcp/tools_checktypes.go:168`).

**The marketing website** (`fclairamb/solidping-website`, separate repo, `origin/main` at 6c7feb3) says **40** in about 60 places and **43** in 3:
- 40 in TSX: `src/pages/index.tsx:36,379,383,534` (36 says "40 protocols"), `src/pages/cloud.tsx:39,106,140,383,736`, `src/pages/pricing.tsx:132`, `src/components/HomepageFeatures/index.tsx:32`, `docusaurus.config.ts:70,168`.
- 40 in translations: `i18n/fr/code.json` (about 10 messages, "40 types de checks" / "40 types de vérification"), `i18n/de/code.json` (about 10 messages, "40 Check-Typen", "40 Protokolle").
- 40 in comparison pages, EN + FR + DE: `src/pages/compare/{better-stack,checkly,healthchecks,maintenant,oh-dear,openstatus,pingdom,pulsetic,site24x7,statuscake,upptime}.md` and their `i18n/{fr,de}/docusaurus-plugin-content-pages/compare/` twins.
- 40 in `docs/marketing/google-ads.md:180,209` (internal notes, not served).
- 43 in `posts/missing-a-check.md` and its FR/DE twins, fixed today by website#63.
- `src/pages/cloud.tsx:107` `CHECK_TYPES` is a chip list "from the README's own list". It is missing VNC, website crawl and health endpoint.

Observed 2026-10-12 while reviewing the posting plan: the README, the docs landing page and the website give three different numbers (43, 39, 40) for the same product.

## Proposal
1. **Mark the uncounted types in the registry.** Add a field to `CheckTypeMeta` (`types.go:371`): `ExcludedFromTally bool \`json:"excludedFromTally"\``, with a doc comment: "not a type a user picks: synthetic (`sleep`) or system-created (`private-location`). Left out of the advertised check-type count." Set it on the `sleep` and `private-location` entries. Do not call it `Internal`: `internal` already means internal checks in `handlers/checks/service.go:439-441`.
2. **One function for the number.** Add `checkerdef.AdvertisedCheckTypes() []CheckType` (registry order, entries without `ExcludedFromTally`) and `checkerdef.AdvertisedCheckTypeCount() int`. Today it returns 43.
3. **Expose the flag on the public API.** Add `ExcludedFromTally bool \`json:"excludedFromTally"\`` to `CheckTypeResponse` (`handlers/checktypes/service.go:14`) and fill it in `toResponse` (`:167`). Additive field on `GET /api/v1/check-types` (public, no auth, `app/server.go:1160`), so the website can read the count from a live instance. Add it to the check-type schema in `server/internal/app/openapi/openapi.yaml`, next to `supportsIpVersion`.
4. **A test that fails on drift.** New `server/internal/checkers/registry/check_type_count_test.go`, reusing `repoRoot` from `docs_anchor_test.go:27`. It reads a fixed list of files, finds every `(?i)\b(\d{2})\+?\s+check[- ]types?\b`, and fails when the number is not `AdvertisedCheckTypeCount()`. The message names file, line, found and expected. Files:
   - `README.md`
   - every `*.md` / `*.mdx` under `web/docs/docs/`
   - `web/docs/docusaurus.config.ts`

   It runs under `make test` (Go tests), so CI catches it with no new Makefile target. Not scanned: `CHANGELOG.md` (history, and "18 check types" there means tunnel support), `specs/`, `wiki/competitors/` (dated snapshots, and full of other products' counts).
5. **Pin the README list too.** In the same test, count the comma-separated items of the `README.md` line that starts with `- **N check types**:`, expanding the grouped ones the README uses ("SSL/Domain", "FTP/SFTP", "SMTP/POP3/IMAP", "7 databases (...)", "3 message queues (...)", "game server (Source/A2S, Minecraft)"). If this parsing turns out brittle, drop it and only check the number. Say so in the PR.
6. **Fix the current occurrences.**
   - `web/docs/docs/intro.md:13`: 39 to 43, and add the missing names to its list (VNC, Prometheus, website crawl, health endpoint, headless browser, custom JS, heartbeat) or end it with "and more" as today.
   - `wiki/architecture.md:163`: drop the number ("one package per check type"). It counts packages, not the tally, and nothing would keep it right.
   - `wiki/competitors/comparison/monitor-types.md:36`, `comparison/overview.md:19` (also bump "counts refreshed" to the PR date and say "45 registered, 2 not counted: sleep and the private-location monitor"), `comparison/solidping-position.md:110`, `positioning.md:126,169,240,260`, `pulsetic.md:66,257`: 40 to 43.
   - Leave `wiki/competitors/indie-watch.md` alone: each line is a dated observation.
7. **Write the rule down where people add types.** `server/internal/checkers/AGENTS.md`, section "Adding New Checkers": set `ExcludedFromTally` for a non-user type; the count test tells you every doc to bump. Replace the `sleep`-only wording at `:24-29` with a pointer to the field.
8. **Reuse the flag for the sleep exemptions, only where behaviour is identical.** `docs_anchor_test.go:21` exempts `sleep` only, and `private-location` does have a docs section, so leave that table as is. `mcp/tools_checktypes.go:173` also excludes `sleep` only, and `private-location` is a valid type there. No change to either.
9. **Website, separate PR in `fclairamb/solidping-website`** (not part of this repo's PR; mention it in the PR description):
   1. Add `src/data/checkTypes.ts` exporting `CHECK_TYPE_COUNT = 43` and the chip list moved out of `cloud.tsx:107`, with VNC, website crawl and health endpoint added.
   2. TSX and `docusaurus.config.ts` import the constant. Translated strings take it as a placeholder (`translate({message: '{count} check types'}, {count})`, `<Translate values={{count}}>`), so `i18n/{fr,de}/code.json` messages carry `{count}` instead of a digit. Run `bun run write-translations` and fix the FR/DE messages by hand.
   3. Markdown pages (`src/pages/compare/*.md` and the FR/DE twins) keep plain text, updated 40 to 43. Add `scripts/check-type-count.mjs`, run in `.github/workflows/ci.yml` before the build. It reads `CHECK_TYPE_COUNT` and fails on any two-digit number from 30 to 99 followed by `check types`, `protocols`, `types de checks?`, `types de check`, `types de vérification`, `Check-Typen` or `Protokolle` that differs from it. The 30 to 99 window is what skips competitor counts (Kuma 13, Peekaping 14, Checkmate ~12, Site24x7 100+). Skip `posts/` and the blog i18n folders: posts are dated.
   4. Bumping the number stays a manual step, documented in the website `README.md`: when solidping's count test changes, bump `CHECK_TYPE_COUNT` and fix what the script flags. Optional later: a weekly scheduled job that compares the constant with `curl -s https://solidping.io/api/v1/check-types | jq '[.data[] | select(.excludedFromTally | not)] | length'` (needs step 3 deployed). Not in the first PR.

Backward compatibility: one additive JSON field. No migration, no config key. Old clients ignore the field.

## Tests
- `server/internal/checkers/checkerdef/types_test.go`, table-driven `TestAdvertisedCheckTypes`:
  - `sleep` is not in `AdvertisedCheckTypes()`.
  - `private-location` is not in it.
  - `http`, `heartbeat`, `email`, `crawl`, `health` are in it.
  - `AdvertisedCheckTypeCount() == len(ListCheckTypeMetas()) - number of entries with ExcludedFromTally`, and that number of excluded entries is 2. A third exclusion should be a deliberate edit to this test.
- `server/internal/checkers/registry/check_type_count_test.go`, `TestAdvertisedCheckTypeCountInDocs`:
  - Passes on the tree after step 6.
  - Table-driven unit test of the matcher on strings: `"43 check types"`, `"**43 check types**"`, `"39 Check Types"`, `"40+ check types"`, `"check-types"` with no number (no match), `"18 check types"` in a line given as CHANGELOG (file not scanned, so no match by construction; test the file list, not the regex, for this one).
  - A fixture where a file says 40 produces a failure message naming the file, line, 40 and 43. Run the scan on a `t.TempDir()` copy, not on the repo.
- README list parsing (step 5): the expanded item count equals `AdvertisedCheckTypeCount()`.
- `server/internal/handlers/checktypes`: a test that `ListServerCheckTypes()` sets `excludedFromTally: true` on `sleep` and `private-location` and false on `http`.
- Website: run `node scripts/check-type-count.mjs` on the tree after the edits (passes) and on a copy with one page left at 40 (fails, names the file).

## To verify
- That `private-location` should stay out of the tally. It is a real check people see in their list, but they never create it by picking a type. The README has left it out since 2026-09-26. If Florent prefers to count it, the number becomes 44 everywhere and only the flag on that one entry changes.
- That the regex in step 4 catches the FR/DE docs pages if `web/docs` gets translations (today `web/docs/docs/` is EN only).
- Website: that Docusaurus `translate()` placeholders work in `docusaurus.config.ts` (config runs in Node, not in React). If not, import the constant and build the string with a template literal there.
- Website: the full list of matches before editing, with `git grep -nE "(3[0-9]|4[0-9]) ?(check types|Check Types|protocols|Protokolle|types de checks?|types de vérification|Check-Typen)"` on `origin/main`. Lines that wrap between the number and the noun are missed by grep; read the compare pages once.

## Acceptance Criteria
- [ ] `checkerdef.AdvertisedCheckTypeCount()` returns 43, and `sleep` and `private-location` carry `ExcludedFromTally`.
- [ ] `GET /api/v1/check-types` returns `excludedFromTally` per type.
- [ ] `go test ./internal/checkers/...` fails if `README.md`, `web/docs/docs/**` or `web/docs/docusaurus.config.ts` states a different count.
- [ ] `web/docs/docs/intro.md` and the listed `wiki/` lines say 43, `wiki/architecture.md` has no count.
- [ ] `server/internal/checkers/AGENTS.md` explains the flag and the test.
- [ ] Website PR opened separately: one `CHECK_TYPE_COUNT`, 43 everywhere, chip list complete, CI script in place.
