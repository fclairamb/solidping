---
model: sonnet
effort: high
---

# HTTP checks with JSONPath assertions cannot be edited in the UI — the form neither shows nor preserves them

## Problem

The backend HTTP checker fully supports `jsonPathAssertions` — a recursive
assertion AST (`assertion` leaves with `path` / `operator` / `value`, and
`and` / `or` group nodes) parsed in
`server/internal/checkers/checkhttp/config.go:311` and evaluated by
`server/internal/checkers/checkhttp/jsonpath.go` (operators: `eq`, `neq`,
`gt`, `gte`, `lt`, `lte`, `contains`, `regex`, `exists`, `not_exists`).

The dashboard, however, never exposes it:

- The HTTP form module (`web/dash0/src/components/checks/form/types/http.tsx`)
  has no JSON-assertions field: `fromConfig` never reads
  `jsonPathAssertions`, and `toConfig` (line 118) **rebuilds the config from
  scratch out of known state only** — so saving an HTTP check from the edit
  form silently deletes any assertions the check carried.
- A ready-made editor component already exists and matches the backend AST
  shape exactly — `web/dash0/src/components/checks/json-assertion-editor.tsx`
  (`JsonAssertionEditor`, with add-assertion / add-group UI) — but it is
  **imported nowhere**. Same for the result-display component
  `web/dash0/src/components/checks/json-assertion-results.tsx`.
- All four locales already carry the strings (`jsonAssertions`,
  `addJsonAssertion`, `jsonPath`, `operator`, `expectedValue`, `addGroup` in
  `web/dash0/src/locales/*/checks.json`), so this looks like a feature that
  was built and never wired into the form.

Net effect, as reported: a check configured with JSONPath assertions (e.g.
dev check `1f70eb57-ef1a-411d-9da6-ba06a43f4239` in org `default`) "can only
be modified through the API" — the UI doesn't show the assertions, and using
the UI to change anything else would destroy them.

## Proposal

Wire JSONPath assertions into the HTTP check form so they round-trip:

1. **State + round-trip** — add the assertion tree to `HttpState`;
   `fromConfig` seeds it from `config.jsonPathAssertions` (accepting the
   snake_case alias `json_path_assertions` the backend also resolves), and
   `toConfig` writes `jsonPathAssertions` back when a tree is present and
   omits the key when the editor is empty/cleared (matching the module's
   omit-at-default style — decide explicitly whether clearing needs
   `jsonPathAssertions: null` to erase a stored value, mirroring how
   `basicAuth: null` clears credentials; verify what the server does with an
   absent key on PATCH and pick the variant that actually clears).
2. **UI** — render `JsonAssertionEditor` in the HTTP form (likely in the
   advanced section alongside the other response-validation fields, next to
   expected status codes), using the existing locale keys. Follow the design
   reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`); if the
   grouped-assertion editor is a new pattern worth cataloguing, add it there.
3. **Results (nice-to-have, same wiring gap)** — if check results carry the
   assertion evaluation (`AssertionResult` in `jsonpath.go`), hook
   `json-assertion-results.tsx` into the check-detail result view so a failed
   assertion is visible; keep this minimal and skip it if results plumbing
   is genuinely absent rather than just unwired.
4. **Tests** — unit-test the `fromConfig`/`toConfig` round-trip (assertions
   survive an edit-and-save with no changes; clearing removes them), and a
   Playwright E2E: create/edit an HTTP check with a JSONPath assertion via
   the UI, save, reload the edit page, and assert the assertion is still
   shown — this is the regression that motivated the spec (silent data loss
   on save).

Out of scope: any change to the backend assertion engine — it already works
via the API. The general "toConfig drops unknown config keys" behaviour is by
design (it intentionally sheds deprecated keys) and is not to be redesigned
here; only `jsonPathAssertions` gains first-class form support.

## Implementation Plan

### Investigation findings (grounding the design decisions below)

- **Canonical key is camelCase, not snake_case.** `HTTPConfig.GetConfig()`
  (`server/internal/checkers/checkhttp/config.go` ~line 464) only ever writes
  `cfg["jsonPathAssertions"]`. `FromMap`'s `resolveKey(configMap,
  "jsonPathAssertions", "json_path_assertions")` checks camelCase first, then
  the snake_case alias, as a read fallback. So `fromConfig` should read
  `config.jsonPathAssertions` first, `config.json_path_assertions` second —
  the reverse priority of `captureFailureResponse` (whose canonical key is
  snake_case).
- **Omitting the key on save is what clears it — no `null` needed.**
  `jsonPathAssertions` is not in `HTTPConfig.SecretFields()`
  (`configKeyBasicAuth`, `configKeyPassword`, `configKeySecretHeaders` only),
  so it never enters `credentials.SecretFieldsFor` and follows the *public*
  key merge rule in `mergePatchConfig` /
  `internal/crypto/credentials/secret_fields.go:MergePatch`: "public keys
  absent from patch are dropped" — i.e. replace-wholesale, same as
  `expectedStatusCodes`. Sending an explicit `jsonPathAssertions: null` would
  also end up clearing it (nil fails `FromMap`'s `v != nil` guard) but would
  leave a stray `"jsonPathAssertions": null` sitting in the merged map before
  `NormalizeConfig` (HTTP's `NormalizeConfig` doesn't touch this key), so
  **omit the key** — matches the module's existing omit-at-default style and
  avoids writing a null key into `checks.config`.
- **Results plumbing exists but is failure-only.** `checker.go` (~line 652)
  evaluates `cfg.JSONPathAssertions.Evaluate(jsonData)` and only attaches
  `"json_path_assertions": assertionResult` to `Output` in the `!pass`
  branch — a passing assertion adds nothing to the result output. So
  `JsonAssertionResults` gets wired as a conditional card (mirrors
  `SslChainCard`/`DnsblCard` in `checks.$checkUid.index.tsx`) that renders
  only when `output.json_path_assertions` is present (i.e. only for a failed
  assertion) — this is not a bug, it's the existing evaluator's contract, so
  the card simply doesn't render on every result.

### Steps

1. **`json-assertion-editor.tsx` / `json-assertion-results.tsx`**: export
   `AssertionNode` / `AssertionResult` (currently module-private, needed by
   `http.tsx` and the new result card) and add `data-testid`s to the leaf
   inputs (`json-assertion-path`, `json-assertion-operator`,
   `json-assertion-value`, `json-assertion-remove`) and group controls
   (`json-assertion-group-type`, `json-assertion-remove-group`,
   `json-assertion-add-in-group`, `json-assertion-add-group`) for E2E
   targeting — none existed since the component was never mounted. Widen the
   leaf path/value inputs to `w-full sm:w-40` so they don't force horizontal
   overflow on mobile.
2. **`http.tsx`**: add `jsonPathAssertions: AssertionNode | null` to
   `HttpState`. `fromConfig` seeds it from `config.jsonPathAssertions` ??
   `config.json_path_assertions` (both validated as an object, else `null`).
   `toConfig` writes `cfg.jsonPathAssertions = state.jsonPathAssertions` only
   when non-null; omits the key otherwise. Render `JsonAssertionEditor` inside
   `HttpOptionsFields` (the Advanced section), under a `jsonAssertions` label
   using the existing locale keys plus one new description key. Extend
   `httpOptionsSummary` so a present assertion tree marks Advanced
   "customized" (auto-opens on edit) and contributes to the summary line.
3. **Locales**: add `jsonAssertionsDescription` to all four
   `locales/*/checks.json` files (the other strings the editor needs already
   exist).
4. **Result card**: add `json-assertion-result-card.tsx` (mirrors
   `dnsbl-card.tsx`'s output-shape type guard) rendering `JsonAssertionResults`
   when `output.json_path_assertions` is a well-formed `AssertionResult`.
   Wire it into `checks.$checkUid.index.tsx` next to the other
   type-conditional cards (`SslChainCard`, `DockerRestartLoopCard`,
   `DnsblCard`), gated on `check.type === "http"`, and filter
   `json_path_assertions` out of the generic raw-output dump the same way
   `chain`/`soonestExpiring`/DNSBL keys are already filtered.
5. **Design reference**: add a `JsonAssertionEditorSection` cataloguing
   `JsonAssertionEditor` (new pattern: recursive assertion-tree editor),
   registered alongside `TokenChipsInputSection`.
6. **Tests**:
   - Unit (`http.test.ts`): `fromConfig` seeds from camelCase and the
     snake_case alias; `toConfig` omits the key when null, writes it when
     present, and a full `toConfig -> fromConfig` round trip preserves an
     assertion tree unchanged (the save-with-no-changes case) and clears it
     when set back to null.
   - Playwright E2E (`check-http-json-assertions.spec.ts`, modeled on
     `check-http-capture-failure-response.spec.ts`): create an HTTP check,
     open Advanced, add a JSON assertion, save, reload the edit page, assert
     the assertion (path/operator/value) is still shown — the regression that
     motivated the spec.
