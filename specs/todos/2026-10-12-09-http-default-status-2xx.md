---
model: sonnet
effort: medium
---

# An HTTP check with no expected status passes only on exactly 200, so healthy 201/202/204 endpoints read as down

## Problem
When neither `expectedStatusCodes` nor the legacy `expectedStatus` is set, the checker defaults to an exact 200 (`server/internal/checkers/checkhttp/checker.go:286-289`) and marks anything else down (`checker.go:744-755`). An endpoint that answers `201 Created`, `202 Accepted` or `204 No Content` is healthy but shows as down until the user finds the status field and types `2XX`.

The dashboard hides the problem. The HTTP form seeds an implicit `["200"]` chip when the config has no status key (`web/dash0/src/components/checks/form/types/http.tsx:122-142`) and omits it again on save (`http.tsx:247-257`). So every check created with the default chip stores no status key and relies on the server default.

The grammar for a range already exists: `NXX` wildcards are validated in `config/statuspattern.go:27-53` and matched by `MatchStatusCode` (`config/config.go:58-80`). Several samples already use `2XX` (`server/internal/checkers/checkhttp/samples.go:15,115`).

## Proposal
1. Server default. In `server/internal/checkers/checkhttp/config/statuspattern.go`, add `DefaultExpectedStatusPattern = "2XX"`. In `checker.go`, remove the `expectedStatus` default (`:286-289`) and rewrite the decision at `:744-755`:
   - `expectedStatusCodes` set: match the patterns (unchanged).
   - else `expectedStatus` != 0: exact match (legacy, unchanged).
   - else: `MatchStatusCode(code, []string{DefaultExpectedStatusPattern})`.
   Point `samples.go:15` at the new constant instead of its own literal.
2. Everything explicit keeps its meaning. A legacy `expectedStatus: 200` stays exact, so the samples that set it (`samples.go:42-82`) do not change. With `followRedirects: false` the final response is the 3xx itself, so it stays down. A chain cut by the 10-hop cap (`checker.go:30,351`) also ends on a 3xx and stays down.
3. Form default. In `http.tsx`:
   - `seedExpectedStatusCodes` returns `["2XX"]` when no key is present (`:141`).
   - The implicit default in `toConfig` becomes an empty list or exactly `["2XX"]` (`:253-254`). An explicit `["200"]` is now written as `expectedStatusCodes: ["200"]`, otherwise picking 200 would silently save 2XX.
   - Placeholder `"200"` becomes `"2XX"` (`:415`). Update the comments at `:58-61`, `:122-129`, `:247-251`.
   An existing check with no status key now shows a `2XX` chip, which matches what the server does after step 1.
4. Paths that need no code change (checked, keep it that way):
   - Config-as-code export writes status keys only when set (`config/config.go:522-528`), so an exported check without one re-imports with the same 2XX default.
   - `ValidateSpec` (`config/validate.go:73-82`), the document validator's exclusivity rule (`server/internal/handlers/checks/validate_document.go:547-566`) and the MCP `validate_check` tool (which calls `ValidateSpec`) apply no default.
   - Importers write codes only when the source has them (`server/internal/handlers/checks/importers/uptimekuma.go:338-339`, `betterstack.go:527-533`, `gatus.go:546-577`). A monitor imported without codes now gets 2XX.
   - Health checks refuse both status keys (`server/internal/checkers/checkhealth/config/validate.go:13-24`) and build their own verdict (`checkhealth/checker.go:78-89`). Unaffected.
   - The `?expectedStatus=` prefill (`web/dash0/src/routes/orgs/$org/checks.new.tsx:92,152`) seeds an explicit chip. Unaffected.
5. Docs, `web/docs/docs/features/check-types.md`: the "Expected Status" row (`:37`) says the default is `2XX` when unset; "Status Code Matching" (`:46-48`) gains a line "Unset: any 2xx response".
6. Behaviour change note. Commit body and changelog entry (`wiki/conventions/changelog.md`) say: an HTTP check with no expected status now passes on any 2xx instead of only 200. It can only turn a down result into up, for 201-299 answers. A user who relied on "204 means broken" must set `expectedStatusCodes: ["200"]`.

## Tests
- `server/internal/checkers/checkhttp/checker_test.go`, table test with no status key: 200, 201, 202 and 204 are up (204 was down before this change); 404 and 500 are down; a 301 with `followRedirects: false` is down.
- Same file, explicit settings still win: `expectedStatusCodes: ["200"]` with a 204 is down; `expectedStatusCodes: ["201"]` with a 200 is down; legacy `expectedStatus: 200` with a 204 is down; `expectedStatusCodes: ["3XX"]` with a 301 and `followRedirects: false` is up.
- Fix any existing checkhttp test that relied on the exact-200 default (grep for a non-200 2xx served with no status key).
- `web/dash0/src/components/checks/form/types/http.test.ts`: the base state (`:19-25`) and the default-seed test (`:64-66`, `:114`) expect `["2XX"]`; `toConfig` omits `["2XX"]` (`:119`); new case: `toConfig` writes `expectedStatusCodes: ["200"]` for an explicit `["200"]`.
- `web/dash0/e2e/check-http-expected-status-codes.spec.ts`: the tests that remove or count the seeded `["200"]` chip (`:98`, `:126`, `:276`, `:299`) use `2XX`. "a plain-200 check saves with neither status key" (`:210-228`) becomes "the default 2XX check saves with neither status key". New test: replacing the default chip with `200` saves `expectedStatusCodes: ["200"]`.
- `make test`, `make lint`, `bun run lint:e2e && bun run typecheck:e2e`.

## Open questions
- Apply the new default to existing checks, or only to checks created after the change (by stamping `expectedStatusCodes: ["200"]` on every existing HTTP check that has no status key)? Recommended: all checks. The change only turns 201-299 downs into ups, and stamping would need a data migration that keeps the surprising behaviour alive on every old check.
